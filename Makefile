.PHONY: build test validate package clean

build:
	go build -o homelab-logging .

test:
	go test ./...

validate:
	go run . --validate

package: test
	go run ./cmd/package

clean:
	rm -f homelab-logging
