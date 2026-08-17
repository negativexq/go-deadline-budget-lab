.PHONY: build vet test race demo

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

demo:
	go run ./cmd/demo
