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

package main

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
)

func taskList(w http.ResponseWriter, r *http.Request) {
	p := writerf(w)
	p("<html><head><title>runsit</title></head>")
	p("<body><h1>running tasks</h1><ul>\n")
	for _, t := range GetTasks() {
		p("<li><a href='/task/%s'>%s</a>: %s</li>\n", t.Name, t.Name,
			html.EscapeString(t.Status()))
	}
	p("</ul></body></html>\n")
}

func taskView(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	taskName := path[len("/task/"):]
	t, ok := GetTask(taskName)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Buffer to memory so we never block writing to a slow client
	// while holding the TaskOutput mutex.
	var buf bytes.Buffer
	p := writerf(&buf)
	defer io.Copy(w, &buf)

	p("<html><head><title>runsit; task %q</title></head>", t.Name)
	p("<body><h1>%v</h1>\n", t.Name)
	p("<p>status: %v</p>", html.EscapeString(t.Status()))

	in, ok := t.RunningInstance()
	if ok {
		p("<p>running instance: pid=%d ", in.Pid())
		p("</p>")
		out := &in.output
		out.mu.Lock()
		defer out.mu.Unlock()
		for e := out.lines.Front(); e != nil; e = e.Next() {
			ol := e.Value.(*outputLine)
			p("<p>%v: %s: %s</p>\n", ol.t, ol.name, html.EscapeString(ol.data))
		}
	}

	p("</body></html>\n")
}

func writerf(w io.Writer) func(string, ...interface{}) {
	return func(format string, args ...interface{}) {
		fmt.Fprintf(w, format, args...)
	}
}

func runWebServer(ln net.Listener) {
	mux := http.NewServeMux()
	// TODO: wrap mux in auth handler, making it available only to
	// TCP connections from localhost and owned by the uid/gid of
	// the running process.
	mux.HandleFunc("/", taskList)
	mux.HandleFunc("/task/", taskView)
	s := &http.Server{
		Handler: mux,
	}
	err := s.Serve(ln)
	if err != nil {
		logger.Fatalf("webserver exiting: %v", err)
	}
}
