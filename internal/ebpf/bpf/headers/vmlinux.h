// Minimal vmlinux.h — only the kernel types needed for Pulse eBPF capture.
// Generated subset from /sys/kernel/btf/vmlinux; keeps the build self-contained.
#pragma once

typedef unsigned char __u8;
typedef unsigned short __u16;
typedef unsigned int __u32;
typedef unsigned long long __u64;
typedef signed char __s8;
typedef signed short __s16;
typedef signed int __s32;
typedef signed long long __s64;

typedef __u16 __be16;
typedef __u32 __be32;
typedef __u32 __wsum;

// Boolean
typedef _Bool bool;
#define true 1
#define false 0

// Socket address families
#define AF_INET 2
#define AF_INET6 10

struct sockaddr {
    unsigned short sa_family;
};

struct sockaddr_in {
    __u16 sin_family;
    __be16 sin_port;
    struct {
        __be32 s_addr;
    } sin_addr;
    __u8 __pad[8];
};

struct sockaddr_in6 {
    __u16 sin6_family;
    __be16 sin6_port;
    __u32 sin6_flowinfo;
    struct {
        union {
            __u8 u6_addr8[16];
            __be32 u6_addr32[4];
        } in6_u;
    } sin6_addr;
    __u32 sin6_scope_id;
};

// Tracepoint struct definitions (stable ABI across kernel versions)
struct trace_event_raw_sys_enter {
    __u64 unused;
    long  id;
    __u64 args[6];
};

struct trace_event_raw_sys_exit {
    __u64 unused;
    long  id;
    long  ret;
};
