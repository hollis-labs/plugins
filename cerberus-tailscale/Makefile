.PHONY: build test clean

build:
	go build -o bin/cerberus-tailscale-plugin ./cmd/cerberus-tailscale-plugin
	./bin/cerberus-tailscale-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml

