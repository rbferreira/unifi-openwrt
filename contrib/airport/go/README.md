# Go on the AirPort's NetBSD 6.0 kernel

`netbsd6-airport.patch` is against Go 1.27.0 (`src/`). Stock Go needs a newer NetBSD: on
Apple's 6.0 kernel a binary dies creating its first thread. With these changes the runtime,
`net`, `net/http` and `os/exec` work there (goroutines, timers, deadlines, subprocesses).

```sh
cp -r "$(go env GOROOT)" goroot && (cd goroot && patch -p1 < ../netbsd6-airport.patch)
GOROOT=$PWD/goroot GOTOOLCHAIN=local GOOS=netbsd GOARCH=arm GOARM=7 CGO_ENABLED=0 \
    goroot/bin/go build -trimpath -ldflags='-s -w' -o uow-netbsd-arm ./cmd/uow
```

| What the kernel does (measured on an A1470, 7.9.1) | Symptom with stock Go | Change |
|---|---|---|
| `ucontext_t` has its `mcontext` at offset 36; NetBSD 7+ pads it to 40 | `_lwp_create` gets a start address where it expects the CPSR: `failed to create new OS thread (errno=-22)` | `runtime/defs1_netbsd_arm.go`: no padding before `uc_mcontext` |
| no `___lwp_park60` (478) | SIGSYS on the first sleep | `runtime/sys_netbsd_arm.s`, `os_netbsd.go`: `___lwp_park50` (434), which takes an absolute wall-clock time |
| a missing system call returns ENOSYS **and** posts SIGSYS to the process (`wait6` from `os/exec`) | the process dies although the library has an ENOSYS fallback | `runtime/signal_netbsd.go`: SIGSYS ignored |
| `SOCK_NONBLOCK` in `socket()` and `paccept()` marks the file but leaves the socket blocking; `F_SETFL` with the flag already set is a no-op | `connect` and `read` block in the kernel, no deadline or timeout ever fires | `net/sock_cloexec.go`, `internal/poll/sock_cloexec.go`: `ioctl(FIONBIO)` after creating the socket |
| a non-blocking `connect` in progress returns EAGAIN, not EINPROGRESS | dial fails at once | `net/fd_unix.go`: EAGAIN treated as in progress |

`struct stat`, `struct kevent` and `timespec` have the layouts Go expects; only the
`ucontext` differs.

Not for upstream Go as it is: the changes are unconditional and would be wrong on a current
NetBSD (`___lwp_park50`, the padding). They are a build for this one kernel.
