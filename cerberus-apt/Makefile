.PHONY: build test clean

build:
	go build -o bin/cerberus-apt-plugin ./cmd/cerberus-apt-plugin
	./bin/cerberus-apt-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
