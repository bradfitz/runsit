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
	"bufio"
	"flag"
	"fmt"
	"io"
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
	cmd    *exec.Cmd
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
		case outputMessage:
			t.Printf("Got output: %#v", m)
		case waitMessage:
			t.onTaskFinished(m)
		}
	}
}

type waitMessage struct {
	cmd *exec.Cmd
	err error // return from cmd.Wait(), nil, *exec.ExitError, or other type
}

type outputMessage struct {
	cmd      *exec.Cmd // instance of command that spoke
	name     string    // "stdout" or "stderr"
	isPrefix bool      // truncated line? (too long)
	data     string    // line or prefix of line
}

type updateMessage struct {
	tf TaskFile
}

type stopMessage struct{}

func (t *Task) Update(tf TaskFile) {
	t.controlc <- updateMessage{tf}
}

// run in task's goroutine
func (t *Task) onTaskFinished(m waitMessage) {
	t.Printf("Task exited; err=%v", m.err)
	if m.cmd == t.cmd {
		t.cmd = nil
	}
	if m.err == nil {
		// TODO: vary sleep time (but not in this goroutine)
		// based on how process ended and when it was last
		// started (prevent crash/restart loops)
	}
	if t.config != nil {
		t.Printf("Restarting")
		t.updateFromConfig(t.config)
	}
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
	t.updateFromConfig(jc)
}

func (t *Task) updateFromConfig(jc jsonconfig.Obj) {
	t.config = nil
	t.stop()

	ports := jc.OptionalObject("ports")
	_ = ports
	user := jc.OptionalString("user", "")
	curUser := os.Getenv("USER")
	if user == "" {
		user = curUser
	}
	if user != curUser {
		panic("TODO: switch user")
	}

	env := []string{}
	stdEnv := jc.OptionalBool("standardEnv", true)
	if stdEnv {
		env = append(env, fmt.Sprintf("USER=%s", user))
	}
	envMap := jc.OptionalObject("env")
	for k, v := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	bin := jc.RequiredString("binary")
	cwd := jc.OptionalString("cwd", "")
	args := jc.OptionalList("args")
	if err := jc.Validate(); err != nil {
		t.Printf("configuration error: %v", err)
		return
	}

	_, err := os.Stat(bin)
	if err != nil {
		t.Printf("stat of binary %q failed: %v", bin, err)
		return
	}

	t.config = jc

	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = env

	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Printf("StdoutPipe: %v", err)
		return
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Printf("StderrPipe: %v", err)
		outPipe.Close()
		return
	}

	err = cmd.Start()
	if err != nil {
		outPipe.Close()
		errPipe.Close()
		t.Printf("Error starting: %v", err)
		return
	}
	t.cmd = cmd
	go t.watchPipe(cmd, outPipe, "stdout")
	go t.watchPipe(cmd, errPipe, "stderr")
	go t.watchCommand(cmd)
}

func (t *Task) watchCommand(cmd *exec.Cmd) {
	err := cmd.Wait()
	t.controlc <- waitMessage{cmd: cmd, err: err}
}

func (t *Task) watchPipe(cmd *exec.Cmd, r io.Reader, name string) {
	br := bufio.NewReader(r)
	for {
		sl, isPrefix, err := br.ReadLine()
		if err != nil {
			t.Printf("pipe %q closed: %v", name, err)
			return
		}
		t.controlc <- outputMessage{
			cmd:      cmd,
			name:     name,
			isPrefix: isPrefix,
			data:     string(sl),
		}
	}
	panic("unreachable")
}

func (t *Task) Stop() {
	t.controlc <- stopMessage{}
}

func (t *Task) stop() {
	if t.cmd == nil {
		return
	}
	t.Printf("sending SIGKILL")
	// TODO: more graceful kill types
	t.cmd.Process.Kill()
	t.cmd = nil
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
