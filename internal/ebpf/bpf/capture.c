//go:build ignore

// Pulse eBPF capture — tracks TCP connections to LLM providers and captures
// HTTP request/response data for trace extraction in userspace.
//
// Hooks:
//   tracepoint/syscalls/sys_enter_connect  — record (pid,fd) → dest mapping
//   tracepoint/syscalls/sys_exit_connect   — confirm connection succeeded
//   tracepoint/syscalls/sys_enter_write    — capture outgoing data on tracked fds
//   tracepoint/syscalls/sys_exit_read      — capture incoming data on tracked fds
//   tracepoint/syscalls/sys_enter_close    — clean up tracked fd

#include "headers/vmlinux.h"
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>

char LICENSE[] SEC("license") = "GPL";

// Capture size per event. 2 KiB is enough for HTTP headers + first bit of body
// (the JSON `usage` object in Anthropic/OpenAI responses is well under that).
// Keep as a power of two so the verifier can prove the AND-mask bound.
#define MAX_DATA_SIZE 2048
#define MAX_CONNECTIONS 10240

// Direction constants for data events.
#define DIR_EGRESS 0
#define DIR_INGRESS 1

// ---------- Shared types (must match Go side) ----------

// Identifies a tracked TCP connection.
struct conn_key {
    __u32 pid;
    __u32 fd;
};

struct conn_info {
    __u32 dst_ip;   // IPv4 big-endian
    __u16 dst_port; // host byte order
    __u64 connect_ts;
};

// Event sent to userspace for each captured data chunk.
// Layout is explicit — padding added so Go-side reading is deterministic.
struct data_event {
    __u64 timestamp;        // 0
    __u32 pid;              // 8
    __u32 fd;               // 12
    __u32 data_len;         // 16
    __u32 dst_ip;           // 20
    __u16 dst_port;         // 24
    __u8  direction;        // 26
    __u8  _pad;             // 27
    __u8  data[MAX_DATA_SIZE]; // 28
};

// ---------- BPF maps ----------

// Active connections to monitored ports.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, MAX_CONNECTIONS);
    __type(key, struct conn_key);
    __type(value, struct conn_info);
} tracked_conns SEC(".maps");

// Ports to monitor (set from userspace). Key=port, value=1.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 16);
    __type(key, __u16);
    __type(value, __u8);
} target_ports SEC(".maps");

// Ring buffer for data events to userspace.
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 24); // 16 MB
} events SEC(".maps");

// Scratch space for connect args (pid_tgid → sockaddr pointer).
struct connect_args {
    __u64 sockaddr_ptr;
    __u32 fd;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, __u64);
    __type(value, struct connect_args);
} active_connects SEC(".maps");

// Scratch for read args (pid_tgid → buf pointer + fd).
struct read_args {
    __u64 buf_ptr;
    __u32 fd;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, __u64);
    __type(value, struct read_args);
} active_reads SEC(".maps");

// ---------- Helpers ----------

static __always_inline __u64 pid_tgid(void) {
    return bpf_get_current_pid_tgid();
}

static __always_inline __u32 pid(void) {
    return pid_tgid() >> 32;
}

// ---------- connect() — track new connections ----------

SEC("tracepoint/syscalls/sys_enter_connect")
int trace_connect_enter(struct trace_event_raw_sys_enter *ctx) {
    __u64 id = pid_tgid();
    int fd = (int)ctx->args[0];
    __u64 sockaddr_ptr = ctx->args[1];

    struct connect_args args = {
        .sockaddr_ptr = sockaddr_ptr,
        .fd = (__u32)fd,
    };
    bpf_map_update_elem(&active_connects, &id, &args, BPF_ANY);
    return 0;
}

SEC("tracepoint/syscalls/sys_exit_connect")
int trace_connect_exit(struct trace_event_raw_sys_exit *ctx) {
    __u64 id = pid_tgid();
    struct connect_args *args = bpf_map_lookup_elem(&active_connects, &id);
    if (!args)
        return 0;

    int ret = ctx->ret;
    // connect() returns 0 on success or -EINPROGRESS for non-blocking.
    if (ret != 0 && ret != -115) { // -EINPROGRESS = -115
        bpf_map_delete_elem(&active_connects, &id);
        return 0;
    }

    // Read the sockaddr to get destination.
    struct sockaddr sa = {};
    bpf_probe_read_user(&sa, sizeof(sa), (void *)args->sockaddr_ptr);

    if (sa.sa_family != AF_INET) {
        bpf_map_delete_elem(&active_connects, &id);
        return 0;
    }

    struct sockaddr_in sin = {};
    bpf_probe_read_user(&sin, sizeof(sin), (void *)args->sockaddr_ptr);

    __u16 port = bpf_ntohs(sin.sin_port);

    // Check if this port is one we're monitoring.
    __u8 *found = bpf_map_lookup_elem(&target_ports, &port);
    if (!found) {
        bpf_map_delete_elem(&active_connects, &id);
        return 0;
    }

    // Track this connection.
    struct conn_key key = {
        .pid = pid(),
        .fd = args->fd,
    };
    struct conn_info info = {
        .dst_ip = sin.sin_addr.s_addr,
        .dst_port = port,
        .connect_ts = bpf_ktime_get_ns(),
    };
    bpf_map_update_elem(&tracked_conns, &key, &info, BPF_ANY);
    bpf_map_delete_elem(&active_connects, &id);
    return 0;
}

// ---------- write() — capture outgoing data on tracked fds ----------

SEC("tracepoint/syscalls/sys_enter_write")
int trace_write_enter(struct trace_event_raw_sys_enter *ctx) {
    int fd = (int)ctx->args[0];
    __u64 buf_ptr = ctx->args[1];
    __u64 count = ctx->args[2];

    struct conn_key key = {
        .pid = pid(),
        .fd = (__u32)fd,
    };
    struct conn_info *info = bpf_map_lookup_elem(&tracked_conns, &key);
    if (!info)
        return 0;

    if (count == 0)
        return 0;

    // Allocate event on ring buffer.
    struct data_event *evt = bpf_ringbuf_reserve(&events, sizeof(struct data_event), 0);
    if (!evt)
        return 0;

    evt->timestamp = bpf_ktime_get_ns();
    evt->pid = pid();
    evt->fd = (__u32)fd;
    evt->dst_ip = info->dst_ip;
    evt->dst_port = info->dst_port;
    evt->direction = DIR_EGRESS;
    evt->data_len = (__u32)count;

    long rc = bpf_probe_read_user(evt->data, MAX_DATA_SIZE, (void *)buf_ptr);
    if (rc < 0) {
        rc = bpf_probe_read_user(evt->data, 256, (void *)buf_ptr);
        if (rc < 0) {
            bpf_ringbuf_discard(evt, 0);
            return 0;
        }
        evt->data_len = 256;
    }

    bpf_ringbuf_submit(evt, 0);
    return 0;
}

// ---------- read() — capture incoming data on tracked fds ----------

SEC("tracepoint/syscalls/sys_enter_read")
int trace_read_enter(struct trace_event_raw_sys_enter *ctx) {
    int fd = (int)ctx->args[0];

    struct conn_key key = {
        .pid = pid(),
        .fd = (__u32)fd,
    };
    struct conn_info *info = bpf_map_lookup_elem(&tracked_conns, &key);
    if (!info)
        return 0;

    __u64 id = pid_tgid();
    struct read_args args = {
        .buf_ptr = ctx->args[1],
        .fd = (__u32)fd,
    };
    bpf_map_update_elem(&active_reads, &id, &args, BPF_ANY);
    return 0;
}

SEC("tracepoint/syscalls/sys_exit_read")
int trace_read_exit(struct trace_event_raw_sys_exit *ctx) {
    __u64 id = pid_tgid();
    struct read_args *args = bpf_map_lookup_elem(&active_reads, &id);
    if (!args)
        return 0;

    long ret_signed = ctx->ret;
    if (ret_signed <= 0) {
        bpf_map_delete_elem(&active_reads, &id);
        return 0;
    }

    struct conn_key key = {
        .pid = pid(),
        .fd = args->fd,
    };
    struct conn_info *info = bpf_map_lookup_elem(&tracked_conns, &key);
    if (!info) {
        bpf_map_delete_elem(&active_reads, &id);
        return 0;
    }

    struct data_event *evt = bpf_ringbuf_reserve(&events, sizeof(struct data_event), 0);
    if (!evt) {
        bpf_map_delete_elem(&active_reads, &id);
        return 0;
    }

    evt->timestamp = bpf_ktime_get_ns();
    evt->pid = pid();
    evt->fd = args->fd;
    evt->dst_ip = info->dst_ip;
    evt->dst_port = info->dst_port;
    evt->direction = DIR_INGRESS;

    // Record the syscall's reported size (may exceed what we actually capture).
    evt->data_len = (__u32)ret_signed;

    // Always read exactly MAX_DATA_SIZE bytes. If the buffer is smaller,
    // bpf_probe_read_user returns a negative error — we still want whatever
    // we can get, so try a couple of sizes. For MVP: fall back to a smaller
    // constant capture.
    long rc = bpf_probe_read_user(evt->data, MAX_DATA_SIZE, (void *)args->buf_ptr);
    if (rc < 0) {
        // Short buffer — try 256 bytes. Enough for HTTP status line + some headers.
        rc = bpf_probe_read_user(evt->data, 256, (void *)args->buf_ptr);
        if (rc < 0) {
            bpf_ringbuf_discard(evt, 0);
            bpf_map_delete_elem(&active_reads, &id);
            return 0;
        }
        evt->data_len = 256;
    }

    bpf_ringbuf_submit(evt, 0);
    bpf_map_delete_elem(&active_reads, &id);
    return 0;
}

// ---------- close() — clean up tracked connections ----------

SEC("tracepoint/syscalls/sys_enter_close")
int trace_close_enter(struct trace_event_raw_sys_enter *ctx) {
    int fd = (int)ctx->args[0];

    struct conn_key key = {
        .pid = pid(),
        .fd = (__u32)fd,
    };
    bpf_map_delete_elem(&tracked_conns, &key);
    return 0;
}
