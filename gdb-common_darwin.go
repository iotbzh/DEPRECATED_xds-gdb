package main

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	syscallEBADE = syscall.EBADEXEC

	syscall_TCGETS = 0x402c7413
	syscall_TCSETS = 0x802c7414
)

func fcntl(fd uintptr, cmd int, arg int) (val int, err error) {
	r, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, uintptr(cmd),
		uintptr(arg))
	val = int(r)
	if e != 0 {
		err = e
	}
	return
}

func tcsetattr(fd uintptr, termios *syscall.Termios) error {
	r, _, e := syscall.Syscall(syscall.SYS_IOCTL,
		fd, uintptr(syscall_TCSETS), uintptr(unsafe.Pointer(termios)))
	if r != 0 {
		return os.NewSyscallError("SYS_IOCTL", e)
	}
	return nil
}

func tcgetattr(fd uintptr, termios *syscall.Termios) error {
	r, _, e := syscall.Syscall(syscall.SYS_IOCTL,
		fd, uintptr(syscall_TCGETS), uintptr(unsafe.Pointer(termios)))
	if r != 0 {
		return os.NewSyscallError("SYS_IOCTL", e)
	}
	return nil
}

func isIgnoredSignal(sig os.Signal) bool {
	return (sig == syscall.SIGWINCH)
}
