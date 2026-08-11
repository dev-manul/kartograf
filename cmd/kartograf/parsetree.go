package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/lang/golang"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/lang/php"
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
