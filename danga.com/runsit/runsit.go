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
	"log"
	"os"
	"net/http"
	"strconv"
)

// Flags.
var (
	httpPort  = flag.Int("http_port", 4762, "HTTP localhost admin port.")
	configDir = flag.String("config_dir", "config", "Directory containing per-task *.json config files.")
)

// TODO: log to multiwriter of stderr and ringbuffer, or maybe just
// ringbuffer depending on the environment.
var logger = log.New(os.Stderr, "", log.Lmicroseconds|log.Lshortfile)

func watchConfigDir() {
	for tf := range dirWatcher().Updates() {
		logger.Printf("task %q updated", tf.Name())
	}
}

func main() {
	flag.Parse()

	go watchConfigDir()

	logger.Printf("Listening on port %d", *httpPort)
	err := http.ListenAndServe(":"+strconv.Itoa(*httpPort), nil)
	if err != nil {
		logger.Printf("Error serving HTTP: %v", err)
	}
}
