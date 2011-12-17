all:
	make -C danga.com/runsit/jsonconfig install
	make -C danga.com/runsit/test/daemon
	make -C danga.com/runsit

