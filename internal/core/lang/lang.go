// Package lang defines the language adapter contract and the adapter
// registry. The core (indexer, storage, MCP server) talks to languages
// exclusively through this interface.
package lang

import (
	"path/filepath"
	"strings"

	"github.com/dev-manul/kartograf/internal/core/model"
)

// ExtractOptions tunes per-file extraction.
type ExtractOptions struct {
	// SkipRefs disables reference/call-edge extraction (symbols and
	// inheritance only). Used for vendor code: its declarations are
	// needed to resolve project hierarchies, but its internal call
	// graph is noise.
	SkipRefs bool
	// Modules maps root-relative directories to module/package names
	// (Go: go.mod roots -> module path; later TS: package.json roots).
	// Adapters use it to build import-path based FQNs.
	Modules map[string]string
}

// Language is implemented once per supported language (php, ts, go, ...).
type Language interface {
	// ID is the stable language identifier used in symbol IDs ("php").
	ID() string
	// Extensions lists file extensions handled by this adapter,
	// with leading dot (".php").
	Extensions() []string
	// ExtractFile parses src and returns the normalized file index.
	// path is used only for reporting; implementations must not read
	// from disk.
	ExtractFile(path string, src []byte, opts ExtractOptions) (*model.FileIndex, error)
}

var registry = map[string]Language{} // extension -> adapter

// Register makes an adapter available to the core. Called from the
// adapter's init() or explicitly at startup.
func Register(l Language) {
	for _, ext := range l.Extensions() {
		registry[strings.ToLower(ext)] = l
	}
}

// ForPath returns the adapter responsible for the given file, or nil.
func ForPath(path string) Language {
	return registry[strings.ToLower(filepath.Ext(path))]
}
