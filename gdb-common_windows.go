package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/Sirupsen/logrus"
)

const (
	syscallEBADE = syscall.EBADE
)

func NewGdbNative(log *logrus.Logger, args []string, env []string) *GdbXds {
	fmt.Printf("Native gdb debug mode not supported on Windows !")
	os.Exit(int(syscall.ENOSYS))

	return nil
}

func isIgnoredSignal(sig os.Signal) bool {
	return false
}
