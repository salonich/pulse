// Package ebpf contains the Pulse eBPF capture agent.
package ebpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64 -cc clang -cflags "-O2 -g -Wall" capture bpf/capture.c -- -I/usr/include/x86_64-linux-gnu -I./bpf
