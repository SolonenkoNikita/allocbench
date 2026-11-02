VERSION ?= dev

.PHONY: build test

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o allocbench ./cmd/allocbench

test:
	go test ./...
