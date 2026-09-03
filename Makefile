.PHONY: build test fmt

build:
	go build -o checkmk-fzf .

test:
	go test ./...

fmt:
	gofmt -w .
