.PHONY: all build test clean

all: test build

build:
	GOWORK=off go run ./scripts/build-catalog.go
	cp catalog.schema.v2.json dist/catalog.schema.v2.json

test:
	GOWORK=off go vet ./...
	GOWORK=off go test -race ./...

clean:
	rm -rf dist
