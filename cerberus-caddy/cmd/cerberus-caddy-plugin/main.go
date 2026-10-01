package main

import (
	"fmt"
	"os"

	"github.com/hollis-labs/cerberus-plugins/caddy/internal/caddyplugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "write-dist" {
		if err := caddyplugin.WriteDist(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "write-dist:", err)
			os.Exit(1)
		}
		return
	}
	if err := subprocess.Serve(caddyplugin.New()); err != nil {
		os.Exit(1)
	}
}
