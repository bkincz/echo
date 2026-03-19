BINARY  := echo
GOFLAGS := -trimpath

.PHONY: build fmt lint test clean

## build: compile the echo binary
build:
	go build $(GOFLAGS) -o $(BINARY) ./cmd/echo

## fmt: format all Go source files
fmt:
	gofmt -w -s ./..
	goimports -w ./..

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## test: run the full test suite
test:
	go test -race -timeout 60s ./...

## clean: remove build artifacts
clean:
	rm -f $(BINARY) $(BINARY).exe
