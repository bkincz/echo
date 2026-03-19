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

// Find returns the name of the first available JS runtime (bun preferred over
// node). Returns "" if neither is found in PATH.
func Find() string {
	once.Do(func() {
		for _, rt := range []string{"bun", "node"} {
			if _, err := exec.LookPath(rt); err == nil {
				runtime = rt
				return
			}
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
		return "", errors.New("Node.js or Bun is required but neither was found in PATH")
	}
	return rt, nil
}
