package main

import (
	"fmt"
	"os"

	"github.com/hollis-labs/cerberus-plugins/contextforge/internal/cfplugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// The binary has two modes. With no arguments it speaks the plugin-sdk
// subprocess protocol on stdio, which is how the host runs it. `write-dist
// <dir>` generates the installable directory at build time, keeping plugin.yaml
// derived from the connector definition instead of hand-maintained.
func main() {
	if len(os.Args) > 2 && os.Args[1] == "write-dist" {
		if err := cfplugin.WriteDist(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "write-dist:", err)
			os.Exit(1)
		}
		return
	}
	if err := subprocess.Serve(cfplugin.New()); err != nil {
		os.Exit(1)
	}
}
