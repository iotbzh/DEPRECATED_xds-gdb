package main

import "os"

// IGDB is an interface for GDB
type IGDB interface {
	Init() (int, error)
	Close() error
	SetConfig(name string, value interface{}) error
	Start(bool) (int, error)
	Cmd() string
	Args() []string
	Env() []string
	OnError(f func(error))
	OnDisconnect(f func(error))
	OnExit(f func(int, error))
	Read(f func(timestamp, stdout, stderr string))
	InferiorRead(f func(timestamp, stdout, stderr string))
	Write(args ...interface{}) error
	SendSignal(sig os.Signal) error
}
