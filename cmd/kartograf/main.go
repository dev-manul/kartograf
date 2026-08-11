// kartograf builds a code map (symbols, references, call graph) for a
// project and serves it to AI coding agents over MCP.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/lang/golang"
	"github.com/dev-manul/kartograf/internal/lang/php"
	"github.com/dev-manul/kartograf/internal/lang/ts"
)

var version = "dev"

func main() {
	php.Register()
	golang.Register()
	ts.Register()

	root := &cobra.Command{
		Use:           "kartograf",
		Short:         "Code map builder and MCP server for AI agents",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newIndexCmd(), newServeCmd(), newEnrichCmd(), newOutlineCmd(), newInstallCmd(), newSelfUpdateCmd(), newParseTreeCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "kartograf:", err)
		os.Exit(1)
	}
}
