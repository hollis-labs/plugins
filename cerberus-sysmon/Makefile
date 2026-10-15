.PHONY: build test clean

build:
	go build -o bin/cerberus-sysmon-plugin ./cmd/cerberus-sysmon-plugin
	./bin/cerberus-sysmon-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml

