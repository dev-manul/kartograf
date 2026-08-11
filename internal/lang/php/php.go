// Package php is the PHP language adapter: tree-sitter based extraction
// of symbols, imports and inheritance facts from PHP source files.
package php

import (
	"fmt"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_php "github.com/tree-sitter/tree-sitter-php/bindings/go"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/lang"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

const langID = "php"

// Adapter implements lang.Language for PHP.
type Adapter struct {
	ts *tree_sitter.Language
}

func New() *Adapter {
	return &Adapter{ts: tree_sitter.NewLanguage(tree_sitter_php.LanguagePHP())}
}

// Register installs the adapter into the global registry.
func Register() { lang.Register(New()) }

func (a *Adapter) ID() string           { return langID }
func (a *Adapter) Extensions() []string { return []string{".php"} }

// Parse returns the raw tree-sitter tree; the caller must Close() it.
// Exposed for debugging (parse-tree command).
func (a *Adapter) Parse(src []byte) (*tree_sitter.Tree, error) {
	p := tree_sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(a.ts); err != nil {
		return nil, fmt.Errorf("php: set language: %w", err)
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("php: parse failed")
	}
	return tree, nil
}

func (a *Adapter) ExtractFile(path string, src []byte, opts lang.ExtractOptions) (*model.FileIndex, error) {
	tree, err := a.Parse(src)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	e := &extractor{
		src:      src,
		fi:       &model.FileIndex{Path: path, Lang: langID},
		skipRefs: opts.SkipRefs,
		refSeen:  map[model.Ref]bool{},
	}
	root := tree.RootNode()
	e.fi.HasErrors = root.HasError()
	e.walkScope(root, newScope(""))
	return e.fi, nil
}
