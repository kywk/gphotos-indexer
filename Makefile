BINARY_NAME=gphotos-indexer
DATA_DIR=./data

.PHONY: all tidy vet test check build auth sync serve clean

all: check

tidy:
	go mod tidy

vet:
	go vet ./...

test:
	go test -v -race ./...

build: tidy
	go build -o bin/$(BINARY_NAME) ./cmd/gphotos-indexer

check: tidy vet test build

auth: build
	./bin/$(BINARY_NAME) auth -data $(DATA_DIR)

sync: build
	./bin/$(BINARY_NAME) sync -data $(DATA_DIR) -max-items 200

serve: build
	./bin/$(BINARY_NAME) serve -data $(DATA_DIR) -port 8080

clean:
	rm -rf bin/
