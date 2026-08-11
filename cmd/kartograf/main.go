// kartograf builds a code map (symbols, references, call graph) for a
// project and serves it to AI coding agents over MCP.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/lang/golang"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/lang/php"
)

var version = "dev"

func main() {
	php.Register()
	golang.Register()

	root := &cobra.Command{
		Use:           "kartograf",
		Short:         "Code map builder and MCP server for AI agents",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newIndexCmd(), newServeCmd(), newOutlineCmd(), newParseTreeCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "kartograf:", err)
		os.Exit(1)
	}
}
