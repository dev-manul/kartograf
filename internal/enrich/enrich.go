// Package enrich adds precision edges from full type-inference tools
// on top of the file-local AST layer: go/types for Go (in-process) and
// PHPStan for PHP (external run, JSONL exchange file).
//
// Enrichment output lives in <project>/.kartograf/enrich.<source>.jsonl —
// a local file the team may commit or gitignore. serve/index re-import
// it automatically when its mtime changes. The indexed root may be a
// workspace holding several projects: every nested .kartograf/ directory
// is discovered, and each exchange file is keyed by its root-relative
// path (origin) so same-source files of different projects coexist.
package enrich

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/indexer"
	"github.com/dev-manul/kartograf/internal/core/store"
)

// Dir is the per-project kartograf directory at the project root.
const Dir = ".kartograf"

const filePrefix, fileSuffix = "enrich.", ".jsonl"

// FilePath returns the exchange file path for a source.
func FilePath(root, source string) string {
	return filepath.Join(root, Dir, filePrefix+source+fileSuffix)
}

// Exchange is one enrichment exchange file as seen from the indexed root.
type Exchange struct {
	// Path is the absolute file path.
	Path string
	// Source is the tool label carried by the edges (phpstan, go-types).
	Source string
	// Prefix is the root-relative directory of the project that owns the
	// file ("" when the file belongs to the root itself). Tool-reported
	// paths are relative to that project and get the prefix on import.
	Prefix string
	// Origin is the root-relative path of the file — the key under which
	// its edges are stored and replaced.
	Origin string
}

// exchangeFor describes an exchange file given its location. A file
// outside the root (ad-hoc import) is treated as the root's own.
func exchangeFor(root, source, path string) Exchange {
	x := Exchange{Path: path, Source: source, Origin: filepath.ToSlash(filepath.Join(Dir, filePrefix+source+fileSuffix))}
	abs, err := filepath.Abs(path)
	if err != nil {
		return x
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return x
	}
	rel = filepath.ToSlash(rel)
	x.Origin = rel
	if dir := filepath.ToSlash(filepath.Dir(filepath.Dir(rel))); dir != "." && filepath.Base(filepath.Dir(rel)) == Dir {
		x.Prefix = dir
	}
	return x
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
// previous edges of the same origin. See Import for path handling.
func ImportFile(s *store.Store, root, source, path string) (int, error) {
	return Import(s, root, exchangeFor(root, source, path))
}

// Import loads one exchange file into the store, replacing all previous
// edges of the same origin. File paths are normalized to root-relative:
// absolute paths under root are relativized, project-relative paths get
// the owning project's prefix, foreign absolute paths (e.g. from a
// docker container) are matched against indexed files by longest
// suffix — again preferring the owning project.
func Import(s *store.Store, root string, x Exchange) (int, error) {
	f, err := os.Open(x.Path)
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
			return 0, fmt.Errorf("%s: bad line %q: %w", x.Path, line[:min(len(line), 80)], err)
		}
		if e.From == "" && e.To == "" {
			continue
		}
		e.File = normalizePath(e.File, root, x.Prefix, indexed)
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
	if err := s.ReplaceExtEdges(x.Origin, x.Source, edges); err != nil {
		return 0, err
	}
	// Record the imported version only when path normalization had an
	// index to work against; an import into a fresh/empty index gets
	// retried by AutoImport after the first real indexing run.
	if len(indexed) > 0 {
		if fi, err := os.Stat(x.Path); err == nil {
			_ = s.SetMeta(mtimeKey(x.Origin), strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		}
	}
	return len(edges), nil
}

func mtimeKey(origin string) string { return "enrich_mtime_" + origin }

// normalizePath maps a tool-reported file path onto a root-relative
// indexed path. prefix is the owning project's root-relative directory.
func normalizePath(p, root, prefix string, indexed map[string]bool) string {
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	if filepath.IsAbs(filepath.FromSlash(p)) {
		if rel, err := filepath.Rel(root, filepath.FromSlash(p)); err == nil && !strings.HasPrefix(rel, "..") {
			if rel = filepath.ToSlash(rel); indexed[rel] {
				return rel
			}
		}
	}
	for _, c := range candidates(prefix, p) {
		if indexed[c] {
			return c
		}
	}
	// Foreign absolute prefix (container mount): strip leading
	// components until a suffix matches an indexed file.
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i := 1; i < len(parts); i++ {
		for _, c := range candidates(prefix, strings.Join(parts[i:], "/")) {
			if indexed[c] {
				return c
			}
		}
	}
	return p
}

// candidates orders the lookups for a relative path: inside the owning
// project first, then as-is from the root.
func candidates(prefix, rel string) []string {
	if prefix == "" || strings.HasPrefix(rel, "/") {
		return []string{rel}
	}
	return []string{prefix + "/" + rel, rel}
}

// Discover finds every .kartograf/enrich.*.jsonl under root: the root's
// own and those of nested projects. The walk skips .git, dependency
// directories, other dot-directories and config excludes (not
// .gitignore: reading that hierarchy costs more than the walk itself),
// so a workspace of many repositories takes a fraction of a second.
// Results are sorted by origin.
func Discover(root string, cfg config.Config) ([]Exchange, error) {
	matcher := indexer.ExcludeMatcher(cfg)
	var out []Exchange
	walk := func(base string) error {
		return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == base {
					return err
				}
				return filepath.SkipDir // unreadable subtree: not our problem
			}
			if !d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			name := d.Name()
			if name == Dir {
				out = append(out, exchangesIn(root, path)...)
				return filepath.SkipDir
			}
			if name == ".git" || strings.HasPrefix(name, ".") || config.VendorDirNames[name] ||
				matcher.Match(strings.Split(rel, "/"), true) {
				return filepath.SkipDir
			}
			return nil
		})
	}
	roots := cfg.Include
	if len(roots) == 0 {
		roots = []string{"."}
	} else {
		// Restricted include set: the root's own exchange files still count.
		out = append(out, exchangesIn(root, filepath.Join(root, Dir))...)
	}
	for _, r := range roots {
		base := filepath.Join(root, filepath.FromSlash(r))
		if _, err := os.Stat(base); err != nil {
			continue
		}
		if err := walk(base); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Origin < out[j].Origin })
	return out, nil
}

// exchangesIn lists the exchange files of one .kartograf directory.
func exchangesIn(root, dir string) []Exchange {
	matches, _ := filepath.Glob(filepath.Join(dir, filePrefix+"*"+fileSuffix))
	var out []Exchange
	for _, path := range matches {
		base := filepath.Base(path)
		source := strings.TrimSuffix(strings.TrimPrefix(base, filePrefix), fileSuffix)
		if source == "" {
			continue
		}
		out = append(out, exchangeFor(root, source, path))
	}
	return out
}

// AutoImport re-imports every discovered exchange file whose mtime
// changed since the last import, and drops edges of files that have
// been deleted. With no files present the layer is simply inactive —
// the AST edges work on their own.
func AutoImport(s *store.Store, root string, cfg config.Config, logf func(format string, args ...any)) error {
	found, err := Discover(root, cfg)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, x := range found {
		present[x.Origin] = true
		fi, err := os.Stat(x.Path)
		if err != nil {
			continue
		}
		last, err := s.Meta(mtimeKey(x.Origin))
		if err != nil {
			return err
		}
		if last == strconv.FormatInt(fi.ModTime().UnixNano(), 10) {
			continue
		}
		n, err := Import(s, root, x)
		if err != nil {
			return fmt.Errorf("import %s: %w", x.Origin, err)
		}
		logf("enrich: imported %d edges from %s", n, x.Origin)
	}
	// A deleted exchange file means the user retired that enrichment:
	// remove its edges instead of serving stale data forever.
	imported, err := s.ImportedEnrichOrigins()
	if err != nil {
		return err
	}
	for _, origin := range imported {
		if present[origin] {
			continue
		}
		if err := s.ReplaceExtEdges(origin, "", nil); err != nil {
			return err
		}
		if err := s.SetMeta(mtimeKey(origin), ""); err != nil {
			return err
		}
		logf("enrich: %s removed, dropped its edges", origin)
	}
	return nil
}
