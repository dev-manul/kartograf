package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/dev-manul/kartograf/internal/lang/golang"
	"github.com/dev-manul/kartograf/internal/lang/php"
	"github.com/dev-manul/kartograf/internal/lang/ts"
)

// parse-tree is a hidden debug command: dump the raw tree-sitter CST
// to verify grammar node kinds when developing extractors.
func newParseTreeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "parse-tree <file>",
		Short:  "Dump the raw tree-sitter parse tree (debug)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var tree *tree_sitter.Tree
			switch strings.ToLower(filepath.Ext(args[0])) {
			case ".php":
				tree, err = php.New().Parse(src)
			case ".go":
				tree, err = golang.New().Parse(src)
			case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
				tree, err = ts.New().Parse(args[0], src)
			default:
				return fmt.Errorf("no parser for %s", args[0])
			}
			if err != nil {
				return err
			}
			defer tree.Close()
			fmt.Println(tree.RootNode().ToSexp())
			return nil
		},
	}
}
