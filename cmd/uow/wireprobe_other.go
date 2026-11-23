// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !linux && !netbsd

package main

// wireProbe needs Linux packet sockets or BPF (wireprobe_linux.go, wireprobe_netbsd.go). Exit 2 = cannot
// tell.
func wireProbe(args []string) int { return 2 }
