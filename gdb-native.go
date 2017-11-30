// +build !windows

/*
 * Copyright (C) 2017 "IoT.bzh"
 * Author Sebastien Douheret <sebastien@iot.bzh>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/kr/pty"
)

// GdbNative - Implementation of IGDB used to interfacing native gdb
type GdbNative struct {
	log   *logrus.Logger
	ccmd  string
	aargs []string
	eenv  []string

	exeCmd *exec.Cmd
	fdPty  *os.File

	// callbacks
	cbOnDisconnect func(error)
	cbRead         func(timestamp, stdout, stderr string)
	cbInferiorRead func(timestamp, stdout, stderr string)
	cbOnExit       func(code int, err error)

	running bool
}

// NewGdbNative creates a new instance of GdbNative
func NewGdbNative(log *logrus.Logger, args []string, env []string) *GdbNative {
	return &GdbNative{
		log:   log,
		ccmd:  "/usr/bin/gdb",
		aargs: args,
		eenv:  env,
	}
}

// SetConfig set additional config fields
func (g *GdbNative) SetConfig(name string, value interface{}) error {
	return fmt.Errorf("Unknown %s field", name)
}

// Init initializes gdb XDS
func (g *GdbNative) Init() (int, error) {

	// Create the exec command
	g.exeCmd = exec.Command(g.ccmd, g.aargs...)

	return 0, nil
}

// Close frees allocated objects and close opened connections
func (g *GdbNative) Close() error {
	g.cbOnDisconnect = nil
	g.cbOnExit = nil
	g.cbRead = nil
	g.cbInferiorRead = nil

	g.running = false

	return nil
}

// Start sends a request to start remotely gdb within xds-server
func (g *GdbNative) Start(inferiorTTY bool) (int, error) {
	var err error

	// Start pty and consequently gdb process
	if g.fdPty, err = pty.Start(g.exeCmd); err != nil {
		return int(syscall.ESPIPE), err
	}

	g.running = true

	// Monitor gdb process EOF
	go func() {
		// Execute command and wait EOF
		err := g.exeCmd.Wait()
		g.cbOnDisconnect(err)
		g.running = false
	}()

	// Handle STDOUT
	go func() {
		sc := bufio.NewScanner(g.fdPty)
		sc.Split(split)
		for sc.Scan() {
			if g.cbRead != nil {
				g.cbRead(time.Now().String(), sc.Text(), "")
			}
			if !g.running {
				return
			}
		}
	}()

	return 0, nil
}

// Cmd returns the command name
func (g *GdbNative) Cmd() string {
	return g.ccmd
}

// Args returns the list of arguments
func (g *GdbNative) Args() []string {
	return g.aargs
}

// Env returns the list of environment variables
func (g *GdbNative) Env() []string {
	return g.eenv
}

// OnError doesn't make sens
func (g *GdbNative) OnError(f func(error)) {
	// nothing to do
}

// OnDisconnect is called when stdin is disconnected
func (g *GdbNative) OnDisconnect(f func(error)) {
	g.cbOnDisconnect = f
}

// OnExit calls when exit event is received
func (g *GdbNative) OnExit(f func(code int, err error)) {
	g.cbOnExit = f
}

// Read calls when a message/string event is received on stdout or stderr
func (g *GdbNative) Read(f func(timestamp, stdout, stderr string)) {
	g.cbRead = f
}

// InferiorRead calls when a message/string event is received on stdout or stderr of the debugged program (IOW inferior)
func (g *GdbNative) InferiorRead(f func(timestamp, stdout, stderr string)) {
	g.cbInferiorRead = f
}

// Write writes message/string into gdb stdin
func (g *GdbNative) Write(args ...interface{}) error {
	s := fmt.Sprint(args...)
	_, err := g.fdPty.Write([]byte(s))
	return err
}

// SendSignal is used to send a signal to remote process/gdb
func (g *GdbNative) SendSignal(sig os.Signal) error {
	if g.exeCmd == nil {
		return fmt.Errorf("exeCmd not initialized")
	}
	return g.exeCmd.Process.Signal(sig)
}

//***** Private functions *****

func split(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	return len(data), data, nil
}
