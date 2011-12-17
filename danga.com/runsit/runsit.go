/*
Copyright 2011 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// runsit runs stuff.
//
// Author: Brad Fitzpatrick <brad@danga.com>

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"danga.com/runsit/jsonconfig"
)

// Flags.
var (
	httpPort  = flag.Int("http_port", 4762, "HTTP localhost admin port.")
	configDir = flag.String("config_dir", "config", "Directory containing per-task *.json config files.")
)

var (
	// TODO: log to multiwriter of stderr and ringbuffer, or maybe just
	// ringbuffer depending on the environment.
	logger = log.New(os.Stderr, "", log.Lmicroseconds|log.Lshortfile)

	tasksMu sync.Mutex
	tasks   = make(map[string]*Task)
)

type Task struct {
	Name     string
	controlc chan interface{}

	// State owned by loop's goroutine:
	config jsonconfig.Obj // last valid config
	p      *exec.Cmd
}

func NewTask(name string) *Task {
	t := &Task{
		Name:     name,
		controlc: make(chan interface{}),
	}
	go t.loop()
	return t
}

func (t *Task) Printf(format string, args ...interface{}) {
	logger.Printf(fmt.Sprintf("Task %q: %s", t.Name, format), args...)
}

func (t *Task) loop() {
	t.Printf("Starting")
	for cm := range t.controlc {
		switch m := cm.(type) {
		case updateMessage:
			t.update(m.tf)
		case stopMessage:
			t.stop()
		}
	}
}

type updateMessage struct {
	tf TaskFile
}

type stopMessage struct {

}

func (t *Task) Update(tf TaskFile) {
	t.controlc <- updateMessage{tf}
}

// run in task's goroutine
func (t *Task) update(tf TaskFile) {
	t.config = nil
	jc, err := jsonconfig.ReadFile(tf.ConfigFileName())
	t.stop()
	if err != nil {
		t.Printf("Bad config file: %v", err)
		return
	}
	t.config = jc
	t.Printf("new config: %#v", jc)
}

func (t *Task) Stop() {
	t.controlc <- stopMessage{}
}

func (t *Task) stop() {
	if t.p == nil {
		return
	}
	t.Printf("sending SIGKILL")
	// TODO: more graceful kill types
	t.p.Process.Kill()
	t.p = nil
}

func watchConfigDir() {
	for tf := range dirWatcher().Updates() {
		t := getTask(tf.Name())
		go t.Update(tf)
	}
}

func getTask(name string) *Task {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[name]
	if !ok {
		t = NewTask(name)
		tasks[name] = t
	}
	return t
}

func main() {
	flag.Parse()

	ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(*httpPort))
	if err != nil {
		logger.Printf("Error listening on port %d: %v", *httpPort, err)
		os.Exit(1)
		return
	}
	logger.Printf("Listening on port %d", *httpPort)

	go watchConfigDir()
	go runWebServer(ln)
	select {}
}
