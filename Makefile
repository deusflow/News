.PHONY: all build test run-local clean

all: build

build:
	mkdir -p bin
	go build -o bin/dknews ./cmd/dknews

test:
	go test -v ./...

run-local:
	./run.sh

clean:
	rm -rf bin

