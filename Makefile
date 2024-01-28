GOPHERJS_GOROOT ?= $(HOME)/.gvm/gos/go1.18/
export GOPHERJS_GOROOT

.PHONY: all
all: static

.PHONY: clean
clean:
	rm -rf static/dmp.js

static: static/dmp.js

static/dmp.js: pkg/dmpweb
	echo $(GOPHERJS_GOROOT)
	gopherjs build -m -o static/dmp.js pkg/dmpweb/dmp.go

