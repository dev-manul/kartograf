// Package enrich adds precision edges from full type-inference tools
// on top of the file-local AST layer: go/types for Go (in-process) and
// PHPStan for PHP (external run, JSONL exchange file).
//
// Enrichment output lives in <root>/.kartograf/enrich.<source>.jsonl —
// a local file the team may commit or gitignore. serve/index re-import
// it automatically when its mtime changes.
package enrich

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/store"
)

// Dir is the per-project kartograf directory at the root.
const Dir = ".kartograf"

// FilePath returns the exchange file path for a source.
func FilePath(root, source string) string {
	return filepath.Join(root, Dir, "enrich."+source+".jsonl")
}

// WriteFile dumps edges as JSONL (replacing the previous file).
func WriteFile(path string, edges []store.ExtEdge) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range edges {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return w.Flush()
}

// ImportFile loads a JSONL exchange file into the store, replacing all
// previous edges of the same source. File paths are normalized to
// root-relative: absolute paths under root are relativized, foreign
// absolute paths (e.g. from a docker container) are matched against
// indexed files by longest suffix.
func ImportFile(s *store.Store, root, source, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	indexed, err := s.IndexedPaths()
	if err != nil {
		return 0, err
	}

	var edges []store.ExtEdge
	seen := map[store.ExtEdge]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e store.ExtEdge
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return 0, fmt.Errorf("%s: bad line %q: %w", path, line[:min(len(line), 80)], err)
		}
		if e.From == "" && e.To == "" {
			continue
		}
		e.File = normalizePath(e.File, root, indexed)
		key := e
		key.Line = 0
		if seen[key] {
			continue
		}
		seen[key] = true
		edges = append(edges, e)
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if err := s.ReplaceExtEdges(source, edges); err != nil {
		return 0, err
	}
	// Record the imported version only when path normalization had an
	// index to work against; an import into a fresh/empty index gets
	// retried by AutoImport after the first real indexing run.
	if len(indexed) > 0 {
		if fi, err := os.Stat(path); err == nil {
			_ = s.SetMeta("enrich_mtime_"+source, strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		}
	}
	return len(edges), nil
}

// normalizePath maps a tool-reported file path onto a root-relative
// indexed path.
func normalizePath(p, root string, indexed map[string]bool) string {
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	if rel, err := filepath.Rel(root, filepath.FromSlash(p)); err == nil && !strings.HasPrefix(rel, "..") {
		rel = filepath.ToSlash(rel)
		if indexed[rel] {
			return rel
		}
	}
	if indexed[p] {
		return p
	}
	// Foreign absolute prefix (container mount): strip leading
	// components until a suffix matches an indexed file.
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i := 1; i < len(parts); i++ {
		suffix := strings.Join(parts[i:], "/")
		if indexed[suffix] {
			return suffix
		}
	}
	return p
}

// AutoImport re-imports every .kartograf/enrich.*.jsonl whose mtime
// changed since the last import, and drops edges of sources whose
// exchange file has been deleted. With no files present the layer is
// simply inactive — the AST edges work on their own.
func AutoImport(s *store.Store, root string, logf func(format string, args ...any)) error {
	matches, err := filepath.Glob(filepath.Join(root, Dir, "enrich.*.jsonl"))
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, path := range matches {
		base := filepath.Base(path)
		source := strings.TrimSuffix(strings.TrimPrefix(base, "enrich."), ".jsonl")
		if source == "" {
			continue
		}
		present[source] = true
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		last, err := s.Meta("enrich_mtime_" + source)
		if err != nil {
			return err
		}
		if last == strconv.FormatInt(fi.ModTime().UnixNano(), 10) {
			continue
		}
		n, err := ImportFile(s, root, source, path)
		if err != nil {
			return fmt.Errorf("import %s: %w", base, err)
		}
		logf("enrich: imported %d edges from %s", n, base)
	}
	// A deleted exchange file means the user retired that enrichment:
	// remove its edges instead of serving stale data forever.
	imported, err := s.ImportedEnrichSources()
	if err != nil {
		return err
	}
	for _, source := range imported {
		if present[source] {
			continue
		}
		if err := s.ReplaceExtEdges(source, nil); err != nil {
			return err
		}
		if err := s.SetMeta("enrich_mtime_"+source, ""); err != nil {
			return err
		}
		logf("enrich: %s.jsonl removed, dropped its edges", "enrich."+source)
	}
	return nil
}
