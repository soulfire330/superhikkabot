BINARY := bin/bot

.PHONY: build run check fmt test clean

build:
	go build -o $(BINARY) ./cmd/bot

run:
	go run ./cmd/bot

check: fmt
	go vet ./...
	go test -race ./...

test:
	go test -race ./...

fmt:
	@unformatted=$$(gofmt -l ./cmd ./internal); \
	if [ -n "$$unformatted" ]; then echo "gofmt нужен: $$unformatted"; exit 1; fi

clean:
	rm -rf bin
