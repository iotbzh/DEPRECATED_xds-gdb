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
