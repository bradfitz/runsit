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
	"fmt"
	"html"
	"net"
	"net/http"
)

func taskList(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "<html><head><title>runsit</title></head>")
	fmt.Fprintf(w, "<body><h1>running tasks</h1><ul>\n")
	for _, t := range GetTasks() {
		fmt.Fprintf(w, "<li><a href='/task/%s'>%s</a>: %s</li>\n", t.Name, t.Name,
			html.EscapeString(t.Status()))
	}
	fmt.Fprintf(w, "</ul></body></html>\n")
}

func taskView(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	taskName := path[len("/task/"):]
	t, ok := GetTask(taskName)
	if !ok {
		http.NotFound(w, r)
		return
	}

	fmt.Fprintf(w, "<html><head><title>runsit; task %q</title></head>", t.Name)
	fmt.Fprintf(w, "<body><h1>%v</h1>\n", t.Name)
	fmt.Fprintf(w, "status: %v", html.EscapeString(t.Status()))
	fmt.Fprintf(w, "</body></html>\n")
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
