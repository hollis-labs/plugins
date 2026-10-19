package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/hollis-labs/plugins/cerberus-postgres-data/internal/dataplugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "projection-version" {
		var p dataplugin.Projection
		d := json.NewDecoder(io.LimitReader(os.Stdin, 65537))
		d.DisallowUnknownFields()
		if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Validate() != nil {
			fmt.Fprintln(os.Stderr, "invalid projection definition")
			os.Exit(1)
		}
		fmt.Println(p.Version())
		return
	}

	if len(os.Args) == 3 && os.Args[1] == "write-dist" {
		if err := dataplugin.WriteDist(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "data distribution could not be written")
			os.Exit(1)
		}
		return
	}
	if err := subprocess.Serve(dataplugin.New()); err != nil {
		os.Exit(1)
	}
}
