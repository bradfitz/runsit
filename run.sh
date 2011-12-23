#!/bin/sh

set -e
go build -x -o ./test/daemon/testdaemon ./test/daemon
go build -x -o runsit
./runsit $@
