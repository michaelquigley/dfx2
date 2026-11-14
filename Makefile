.DEFAULT_GOAL := build
export CGO_ENABLED := 0

.PHONY: build test clean

build:
	go install ./...

test:
	go test ./... -count=1
	go vet ./...

clean:
	go clean ./...
	go clean -i ./examples/mixer ./examples/lifetime ./examples/webview
