package main

import (
	"fmt"
	"os"

	"github.com/hollis-labs/plugins/cerberus-postgres/internal/pgplugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "write-dist" {
		if err := pgplugin.WriteDist(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "write-dist:", err)
			os.Exit(1)
		}
		return
	}
	if err := subprocess.Serve(pgplugin.New()); err != nil {
		os.Exit(1)
	}
}
