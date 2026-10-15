.PHONY: build test

build:
	go build -o bin/cerberus-caddy-plugin ./cmd/cerberus-caddy-plugin
	./bin/cerberus-caddy-plugin write-dist .

test:
	go test ./...
