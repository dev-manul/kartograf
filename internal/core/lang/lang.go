// Package lang defines the language adapter contract and the adapter
// registry. The core (indexer, storage, MCP server) talks to languages
// exclusively through this interface.
package lang

import (
	"path/filepath"
	"strings"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

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
	ExtractFile(path string, src []byte) (*model.FileIndex, error)
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
