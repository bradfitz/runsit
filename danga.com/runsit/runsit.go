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
	"container/list"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

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

// A Task is a named daemon. A single instance of Task exists for the
// life of the runsit daemon, despite how many times the task has
// failed and restarted.
type Task struct {
	// Immutable:
	Name     string
	controlc chan interface{}

	// State owned by loop's goroutine:
	config   jsonconfig.Obj // last valid config
	running  *TaskInstance
	failures []*TaskInstance // last few failures, oldest first.
}

// TaskInstance is a particular instance of a running Task.
type TaskInstance struct {
	task      *Task          // set once; not goroutine safe (may only call public methods)
	startTime time.Time      // set once; immutable
	config    jsonconfig.Obj // set once; immutable
	cmd       *exec.Cmd      // set once; immutable
	output    TaskOutput     // internal locking, safe for concurrent access
}

// ID returns a unique ID string for this task instance.
func (in *TaskInstance) ID() string {
	// TODO: include pid if available
	return fmt.Sprintf("%s/%d", in.task.Name, in.startTime.Unix())
}

func (in *TaskInstance) Printf(format string, args ...interface{}) {
	logger.Printf(fmt.Sprintf("Task %s: %s", in.ID(), format), args...)
}

// TaskOutput is the output
type TaskOutput struct {
	mu    sync.Mutex
	lines list.List // of *outputLine
}

func (to *TaskOutput) Add(o *outputLine) {
	to.mu.Lock()
	defer to.mu.Unlock()
	to.lines.PushBack(o)
	const maxKeepLines = 5000
	if to.lines.Len() > maxKeepLines {
		to.lines.Remove(to.lines.Front())
	}
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
		case statusRequestMessage:
			m.resCh <- t.status()
		case runningRequestMessage:
			m.resCh <- t.running
		case updateMessage:
			t.update(m.tf)
		case stopMessage:
			t.stop()
		case waitMessage:
			t.onTaskFinished(m)
		}
	}
}

type waitMessage struct {
	instance *TaskInstance
	err      error // return from cmd.Wait(), nil, *exec.ExitError, or other type
}

type outputLine struct {
	t        time.Time
	instance *TaskInstance
	name     string // "stdout" or "stderr"
	isPrefix bool   // truncated line? (too long)
	data     string // line or prefix of line
}

type updateMessage struct {
	tf TaskFile
}

type stopMessage struct{}

type statusRequestMessage struct {
	resCh chan<- string
}

type runningRequestMessage struct {
	resCh chan<- *TaskInstance
}

func (t *Task) Update(tf TaskFile) {
	t.controlc <- updateMessage{tf}
}

// run in Task.loop
func (t *Task) onTaskFinished(m waitMessage) {
	t.Printf("Task exited; err=%v", m.err)
	if m.instance == t.running {
		t.running = nil
	}
	const keepFailures = 5
	if len(t.failures) == keepFailures {
		copy(t.failures, t.failures[1:])
		t.failures = t.failures[:keepFailures-1]
	}
	t.failures = append(t.failures, m.instance)

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

// run in Task.loop
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

// run in Task.loop
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
	t.config = jc

	_, err := os.Stat(bin)
	if err != nil {
		t.Printf("stat of binary %q failed: %v", bin, err)
		return
	}

	instance := &TaskInstance{
		task:      t,
		config:    jc,
		startTime: time.Now(),
		cmd:       exec.Command(bin, args...),
	}

	cmd := instance.cmd
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
	t.running = instance
	go instance.watchPipe(outPipe, "stdout")
	go instance.watchPipe(errPipe, "stderr")
	go instance.awaitDeath()
}

// run in its own goroutine
func (in *TaskInstance) awaitDeath() {
	err := in.cmd.Wait()
	in.task.controlc <- waitMessage{instance: in, err: err}
}

// run in its own goroutine
func (in *TaskInstance) watchPipe(r io.Reader, name string) {
	br := bufio.NewReader(r)
	for {
		sl, isPrefix, err := br.ReadLine()
		if err != nil {
			in.Printf("pipe %q closed: %v", name, err)
			return
		}
		in.output.Add(&outputLine{
			t:        time.Now(),
			instance: in,
			name:     name,
			isPrefix: isPrefix,
			data:     string(sl),
		})
	}
	panic("unreachable")
}

func (t *Task) Stop() {
	t.controlc <- stopMessage{}
}

// runs in Task.loop
func (t *Task) stop() {
	in := t.running
	if in == nil {
		return
	}
	in.Printf("sending SIGKILL")
	// TODO: more graceful kill types
	in.cmd.Process.Kill()
	t.running = nil
}

func (t *Task) Status() string {
	ch := make(chan string, 1)
	t.controlc <- statusRequestMessage{resCh: ch}
	return <-ch
}

func (t *Task) RunningInstance() (*TaskInstance, bool) {
	ch := make(chan *TaskInstance, 1)
	t.controlc <- runningRequestMessage{resCh: ch}
	in := <-ch
	return in, in != nil
}

// runs in Task.loop
func (t *Task) status() string {
	in := t.running
	if in != nil {
		d := time.Now().Sub(in.startTime)
		return fmt.Sprintf("running; for %v", d)
	}
	if t.config == nil {
		return "not running, no valid config"
	}
	// TODO: flesh these not running states out.
	// e.g. intentionaly stopped, how long we're pausing before
	// next re-start attempt, etc.
	return "not running; valid config"
}

func watchConfigDir() {
	for tf := range dirWatcher().Updates() {
		t := GetOrMakeTask(tf.Name())
		go t.Update(tf)
	}
}

func GetTask(name string) (*Task, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[name]
	return t, ok
}

// GetOrMakeTask returns or create the named task.
func GetOrMakeTask(name string) *Task {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[name]
	if !ok {
		t = NewTask(name)
		tasks[name] = t
	}
	return t
}

// GetTasks returns all known tasks.
func GetTasks() []*Task {
	ts := []*Task{}
	tasksMu.Lock()
	defer tasksMu.Unlock()
	for _, t := range tasks {
		ts = append(ts, t)
	}
	return ts
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
