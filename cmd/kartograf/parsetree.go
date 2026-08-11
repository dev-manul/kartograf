package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/lang/php"
)

// parse-tree is a hidden debug command: dump the raw tree-sitter CST
// to verify grammar node kinds when developing extractors.
func newParseTreeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "parse-tree <file.php>",
		Short:  "Dump the raw tree-sitter parse tree (debug)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			tree, err := php.New().Parse(src)
			if err != nil {
				return err
			}
			defer tree.Close()
			fmt.Println(tree.RootNode().ToSexp())
			return nil
		},
	}
}
