.PHONY: build test clean

build:
	go build -o bin/cerberus-postgres-plugin ./cmd/cerberus-postgres-plugin
	./bin/cerberus-postgres-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
