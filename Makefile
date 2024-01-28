.PHONY: all
all: static

.PHONY: clean
clean:
	rm -rf static/dmp.wasm

static: static/dmp.wasm

static/dmp.wasm: pkg/dmpweb
	tinygo build -o static/dmp.wasm -target wasm ./pkg/dmpweb

