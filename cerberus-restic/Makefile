.PHONY: build test clean

build:
	go build -o bin/cerberus-restic-plugin ./cmd/cerberus-restic-plugin
	./bin/cerberus-restic-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
