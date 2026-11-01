//go:build e2e

package main

// The scenario runner (e2e/) drives a build made with -tags e2e. This file is
// that build's only difference from a release: it links the automation plugin,
// which listens only when MYGO_AUTOMATION names a socket, and, when
// ARBOR_PERF_LOG names a file, writes every perf span to it as a JSON line. It
// is excluded from every build without the e2e tag, release builds included.

import (
	"arbor/internal/perflog"
	"fmt"
	"os"

	_ "github.com/egoist/mygo/plugins/automation"
)

func init() {
	path := os.Getenv("ARBOR_PERF_LOG")
	if path == "" {
		return
	}
	if _, err := perflog.Install(path); err != nil {
		fmt.Fprintf(os.Stderr, "arbor: ARBOR_PERF_LOG: %v\n", err)
	}
}
