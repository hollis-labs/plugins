.PHONY: build test clean

build:
	go build -o bin/cerberus-ufw-plugin ./cmd/cerberus-ufw-plugin
	./bin/cerberus-ufw-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
