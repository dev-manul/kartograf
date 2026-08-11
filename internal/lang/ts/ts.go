// Package ts is the TypeScript/JavaScript language adapter: tree-sitter
// based extraction of symbols, imports and reference edges from
// .ts/.tsx/.js/.jsx/.mjs/.cjs sources.
//
// In JS the module is the file, so FQNs are path-based:
// "src/api/client#ApiClient", "src/api/client#ApiClient.get()",
// "src/api/client#formatUser()". The module path is the root-relative
// file path with the extension stripped and a trailing "/index"
// removed — that makes relative imports line up with symbol FQNs
// without any cross-file resolution.
//
// ExtractOptions.Modules maps workspace package roots to their
// package.json names (collected by the indexer), so imports like
// "@scope/ui-kit/button" resolve to the package's directory.
package ts

import (
	"fmt"
	"path"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/lang"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

const langID = "ts"

// Adapter implements lang.Language for the TS/JS family.
type Adapter struct {
	typescript *tree_sitter.Language
	tsx        *tree_sitter.Language
	javascript *tree_sitter.Language
}

func New() *Adapter {
	return &Adapter{
		typescript: tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
		tsx:        tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()),
		javascript: tree_sitter.NewLanguage(tree_sitter_javascript.Language()),
	}
}

// Register installs the adapter into the global registry.
func Register() { lang.Register(New()) }

func (a *Adapter) ID() string { return langID }

func (a *Adapter) Extensions() []string {
	return []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}
}

func (a *Adapter) grammarFor(filePath string) *tree_sitter.Language {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".tsx":
		return a.tsx
	case ".js", ".jsx", ".mjs", ".cjs":
		return a.javascript
	default:
		return a.typescript
	}
}

// Parse returns the raw tree-sitter tree; the caller must Close() it.
func (a *Adapter) Parse(filePath string, src []byte) (*tree_sitter.Tree, error) {
	p := tree_sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(a.grammarFor(filePath)); err != nil {
		return nil, fmt.Errorf("ts: set language: %w", err)
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("ts: parse failed")
	}
	return tree, nil
}

// modulePath converts a file path into the FQN module prefix.
func modulePath(filePath string) string {
	p := filePath
	for _, ext := range []string{".d.ts", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(p, ext) {
			p = strings.TrimSuffix(p, ext)
			break
		}
	}
	return strings.TrimSuffix(p, "/index")
}

func (a *Adapter) ExtractFile(filePath string, src []byte, opts lang.ExtractOptions) (*model.FileIndex, error) {
	tree, err := a.Parse(filePath, src)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	// Reverse the indexer's dir -> package-name map for import
	// resolution by package name.
	pkgDirs := map[string]string{}
	for dir, name := range opts.Modules {
		if name != "" {
			pkgDirs[name] = dir
		}
	}

	e := &extractor{
		src:      src,
		fi:       &model.FileIndex{Path: filePath, Lang: langID},
		mod:      modulePath(filePath),
		fileDir:  path.Dir(filePath),
		skipRefs: opts.SkipRefs,
		pkgDirs:  pkgDirs,
		imports:  map[string]importTarget{},
		locals:   map[string]bool{},
		refSeen:  map[model.Ref]bool{},
	}
	root := tree.RootNode()
	e.fi.HasErrors = root.HasError()
	e.walkFile(root)
	return e.fi, nil
}
