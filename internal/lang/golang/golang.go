// Package golang is the Go language adapter: tree-sitter based
// extraction of symbols, imports and reference edges from Go sources.
//
// FQNs follow the import-path convention: "module/pkg/dir.Type",
// "module/pkg/dir.Type.Method()", "module/pkg/dir.Func()". The module
// path for a file is resolved from ExtractOptions.Modules (dir ->
// module map collected by the indexer from go.mod files).
package golang

import (
	"fmt"
	"path"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/dev-manul/kartograf/internal/core/lang"
	"github.com/dev-manul/kartograf/internal/core/model"
)

const langID = "go"

// Adapter implements lang.Language for Go.
type Adapter struct {
	ts *tree_sitter.Language
}

func New() *Adapter {
	return &Adapter{ts: tree_sitter.NewLanguage(tree_sitter_go.Language())}
}

// Register installs the adapter into the global registry.
func Register() { lang.Register(New()) }

func (a *Adapter) ID() string           { return langID }
func (a *Adapter) Extensions() []string { return []string{".go"} }

// Parse returns the raw tree-sitter tree; the caller must Close() it.
func (a *Adapter) Parse(src []byte) (*tree_sitter.Tree, error) {
	p := tree_sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(a.ts); err != nil {
		return nil, fmt.Errorf("go: set language: %w", err)
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("go: parse failed")
	}
	return tree, nil
}

// packagePath resolves the import path of the package containing the
// file: the module of the nearest enclosing go.mod plus the directory
// below it. Falls back to the plain directory path when no module map
// entry covers the file.
func packagePath(filePath string, modules map[string]string) string {
	dir := path.Dir(filePath)
	if dir == "." {
		dir = ""
	}
	best, bestModule := -1, ""
	for root, mod := range modules {
		norm := root
		if norm == "." {
			norm = ""
		}
		if dir == norm || strings.HasPrefix(dir, norm+"/") || norm == "" {
			if len(norm) > best {
				best, bestModule = len(norm), mod
				if sub := strings.TrimPrefix(strings.TrimPrefix(dir, norm), "/"); sub != "" {
					bestModule = mod + "/" + sub
				}
			}
		}
	}
	if best >= 0 {
		return bestModule
	}
	return dir
}

func (a *Adapter) ExtractFile(filePath string, src []byte, opts lang.ExtractOptions) (*model.FileIndex, error) {
	tree, err := a.Parse(src)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	e := &extractor{
		src:        src,
		fi:         &model.FileIndex{Path: filePath, Lang: langID},
		pkg:        packagePath(filePath, opts.Modules),
		skipRefs:   opts.SkipRefs,
		imports:    map[string]string{},
		fieldTypes: map[string]map[string]string{},
		refSeen:    map[model.Ref]bool{},
	}
	root := tree.RootNode()
	e.fi.HasErrors = root.HasError()
	e.walkFile(root)
	return e.fi, nil
}
