package jsruntime

import (
	"errors"
	"os/exec"
	"sync"
)

var (
	once    sync.Once
	runtime string
)

// Find returns "node" when Node.js is available in PATH, otherwise "".
func Find() string {
	once.Do(func() {
		if _, err := exec.LookPath("node"); err == nil {
			runtime = "node"
		}
	})
	return runtime
}

// Require returns the JS runtime name or an error if none is available.
// Use this when a JS runtime is mandatory for the operation (e.g. executing
// echo.config.ts or compiling .loader.ts files).
func Require() (string, error) {
	rt := Find()
	if rt == "" {
		return "", errors.New("Node.js is required but was not found in PATH")
	}
	return rt, nil
}
