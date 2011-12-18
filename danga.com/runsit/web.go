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
	"html/template"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func taskList(w http.ResponseWriter, r *http.Request) {
	drawTemplate(w, "taskList", tmplData{
		"Title": "Tasks",
		"Tasks": GetTasks(),
		"Log":   logBuf.String(),
	})
}

func killTask(w http.ResponseWriter, r *http.Request, t *Task) {
	in, ok := t.RunningInstance()
	if !ok {
		http.Error(w, "task not running", 500)
		return
	}
	pid, _ := strconv.Atoi(r.FormValue("pid"))
	if in.Pid() != pid || pid == 0 {
		http.Error(w, "active task pid doesn't match pid parameter", 500)
		return
	}
	in.cmd.Process.Kill()
	drawTemplate(w, "killTask", tmplData{
		"Title": "Kill",
		"Task":  t,
		"PID":   pid,
	})
}

func taskView(w http.ResponseWriter, r *http.Request) {
	taskName := r.URL.Path[len("/task/"):]
	t, ok := GetTask(taskName)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mode := r.FormValue("mode")
	switch mode {
	case "kill":
		killTask(w, r, t)
		return
	default:
		http.Error(w, "unknown mode", 400)
		return
	case "":
	}

	data := tmplData{
		"Title": "Task: " + t.Name,
		"Task":  t,
	}

	in, ok := t.RunningInstance()
	if ok {
		data["PID"] = in.Pid()
		data["Lines"] = in.output.lineSlice()
		data["Cmd"] = in.cmd
	}

	drawTemplate(w, "viewTask", data)
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

type tmplData map[string]interface{}

func drawTemplate(w io.Writer, name string, data tmplData) {
	err := templates[name].ExecuteTemplate(w, "root", data)
	if err != nil {
		logger.Println(err)
	}
}

var templates = make(map[string]*template.Template)

func init() {
	for name, html := range templateHTML {
		t := template.New(name).Funcs(templateFuncs)
		template.Must(t.Parse(rootHTML))
		template.Must(t.Parse(`{{define "body"}}` + html + `{{end}}`))
		templates[name] = t
	}
}

const rootHTML = `
{{define "root"}}
<html>
	<head>
		<title>{{.Title}} - runsit</title>
		<style>
		#output {
		   font-family: monospace;
		   font-size: 10pt;
		   border: 2px solid gray;
		   padding: 0.5em;
		   overflow: scroll;
		   max-height: 25em;
		}

		#output div.stderr {
		   color: #c00;
		}
		</style>
	</head>
	<body>
		<h1>{{.Title}}</h1>
		{{template "body" .}}
	</body>
</html>
{{end}}
`

var templateHTML = map[string]string{
	"taskList": `
		<h2>Running</h2>
		<ul>
		{{range .Tasks}}
			<li><a href="/task/{{.Name}}">{{.Name}}</a>: {{.Status}}</li>
		{{end}}
		</ul>
		<h2>Log</h2>
		<pre>{{.Log}}</pre>
`,
	"killTask": `
		<p>Killed pid {{.PID}}.</p>
		<p>Back to <a href='/task/{{.Task.Name}}'>{{.Task.Name}} status</a>.</p>
`,
	"viewTask": `
		<div>[<a href='/'>Tasks</a>]</div>
		<p>Status: {{.Task.Status}}</p>

		{{if .PID}}
		<p>running instance: pid={{.PID}}
		[<a href='/task/{{.Task.Name}}?pid={{.PID}}&mode=kill'>kill</a>]</p>
		{{end}}

		{{with .Cmd}}
		{{/* TODO: embolden arg[0] */}}
		<p>command: {{range .Args}}{{maybeQuote .}} {{end}}</p>
		{{end}}

		{{with .Lines}}
		<div id='output'>
		{{range .}}
			<div class='{{.Name}}' title='{{.T}}'>{{.Data}}</div>
		{{end}}
		</div>
		{{end}}

		<script>
		window.addEventListener("load", function() {
		   var d = document.getElementById("output");
		   if (d) {
		     d.scrollTop = d.scrollHeight;
		   }
		});
		</script>
`,
}

var templateFuncs = template.FuncMap{
	"maybeQuote": maybeQuote,
}

func maybeQuote(s string) string {
	if strings.Contains(s, " ") || strings.Contains(s, `"`) {
		return fmt.Sprintf("%q", s)
	}
	return s
}
