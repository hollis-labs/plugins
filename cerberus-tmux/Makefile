.PHONY: build test clean

build:
	go build -o bin/cerberus-tmux-plugin ./cmd/cerberus-tmux-plugin
	./bin/cerberus-tmux-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
