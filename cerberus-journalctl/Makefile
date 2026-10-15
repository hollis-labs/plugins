.PHONY: build test clean

build:
	go build -o bin/cerberus-journalctl-plugin ./cmd/cerberus-journalctl-plugin
	./bin/cerberus-journalctl-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
