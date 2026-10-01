.PHONY: build test clean

build:
	go build -o bin/cerberus-rsync-plugin ./cmd/cerberus-rsync-plugin
	./bin/cerberus-rsync-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
