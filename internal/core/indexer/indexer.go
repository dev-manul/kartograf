// Package indexer walks a project tree, runs language adapters over
// changed files and persists results into the store. Incrementality is
// content-hash based: unchanged files are never re-extracted, files
// gone from disk are removed from the index.
package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	gitignore "github.com/go-git/go-git/v5/plumbing/format/gitignore"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/config"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/lang"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/store"
)

type Options struct {
	Root  string // absolute project root
	Store *store.Store
	Cfg   config.Config
	// Force re-extracts every file regardless of stored hashes.
	Force bool
	// Log, when set, receives progress lines.
	Log func(format string, args ...any)
}

type Stats struct {
	Scanned   int // candidate files seen on disk
	Indexed   int // extracted and (re)written
	Unchanged int
	Removed   int // present in DB, gone from disk
	Symbols   int // symbols written this run
	ParseErr  int // files indexed best-effort due to parse errors
	Duration  time.Duration
}

type entry struct {
	rel     string // root-relative, forward slashes
	vendor  bool
	size    int64
	mtimeNs int64
}

type result struct {
	entry
	unchanged bool
	touch     bool // unchanged content but stale stat info in DB
	data      store.FileData
	err       error
}

// Run indexes the project incrementally and returns run statistics.
func Run(opts Options) (*Stats, error) {
	start := time.Now()
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	known, err := opts.Store.FileStates()
	if err != nil {
		return nil, err
	}

	entries, modules, err := collect(opts.Root, opts.Cfg)
	if err != nil {
		return nil, err
	}
	stats := &Stats{Scanned: len(entries)}
	logf("scanning %d files", len(entries))

	jobs := make(chan entry)
	results := make(chan result, 64)

	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				results <- process(opts, known, modules, e)
			}
		}()
	}
	go func() {
		for _, e := range entries {
			jobs <- e
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	// A fresh database takes the bulk path: secondary indexes and FTS
	// are built once after the load instead of per row.
	var w *store.Writer
	if len(known) == 0 {
		w, err = opts.Store.BeginBulkWrite()
	} else {
		w, err = opts.Store.BeginWrite()
	}
	if err != nil {
		return nil, err
	}
	defer w.Rollback()

	seen := make(map[string]bool, len(entries))
	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", r.rel, r.err)
			}
			continue
		}
		seen[r.rel] = true
		if r.unchanged {
			stats.Unchanged++
			if r.touch {
				if err := w.TouchFile(r.rel, r.size, r.mtimeNs); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := w.ReplaceFile(r.data); err != nil {
			return nil, fmt.Errorf("%s: %w", r.rel, err)
		}
		stats.Indexed++
		stats.Symbols += len(r.data.FI.Symbols)
		if r.data.FI.HasErrors {
			stats.ParseErr++
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}

	for path := range known {
		if !seen[path] {
			if err := w.DeleteFile(path); err != nil {
				return nil, err
			}
			stats.Removed++
		}
	}

	if err := w.Commit(); err != nil {
		return nil, err
	}
	stats.Duration = time.Since(start)
	return stats, nil
}

func process(opts Options, known map[string]store.FileMeta, modules map[string]string, e entry) result {
	prev, ok := known[e.rel]
	// Stat fast path: same size and mtime as last time — assume
	// unchanged without reading the file (same trade-off git makes).
	if ok && !opts.Force && prev.Size == e.size && prev.MtimeNs == e.mtimeNs {
		return result{entry: e, unchanged: true}
	}
	data, err := os.ReadFile(filepath.Join(opts.Root, filepath.FromSlash(e.rel)))
	if err != nil {
		return result{entry: e, err: err}
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if ok && !opts.Force && prev.Hash == hash {
		// Content identical, only stat info drifted (branch switch and
		// back): refresh it so future runs stay on the fast path.
		return result{entry: e, unchanged: true, touch: true}
	}
	adapter := lang.ForPath(e.rel)
	if adapter == nil {
		return result{entry: e, err: fmt.Errorf("no language adapter")}
	}
	// Vendor code is indexed shallow: declarations and hierarchy only,
	// its internal call graph is noise.
	fi, err := adapter.ExtractFile(e.rel, data, lang.ExtractOptions{SkipRefs: e.vendor, Modules: modules})
	if err != nil {
		return result{entry: e, err: err}
	}
	return result{entry: e, data: store.FileData{
		FI:      fi,
		Hash:    hash,
		Size:    int64(len(data)),
		MtimeNs: e.mtimeNs,
		Vendor:  e.vendor,
		Indexed: time.Now().Unix(),
	}}
}

// collect walks the project and returns candidate files plus the
// module map (dir -> module name from go.mod files met on the way).
// Non-vendor paths respect .gitignore (all nested files) plus config
// excludes; vendor directories bypass gitignore (they are usually
// ignored) and are included or skipped wholesale per config.
func collect(root string, cfg config.Config) ([]entry, map[string]string, error) {
	matcher := buildMatcher(root, cfg)

	roots := cfg.Include
	if len(roots) == 0 {
		roots = []string{"."}
	}

	var entries []entry
	modules := map[string]string{}
	for _, r := range roots {
		base := filepath.Join(root, filepath.FromSlash(r))
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			vendor := inVendor(rel)
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				if vendor {
					if cfg.Vendor == "skip" {
						return filepath.SkipDir
					}
					return nil
				}
				if matcher.Match(strings.Split(rel, "/"), true) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "go.mod" && !vendor {
				if mod := readModulePath(path); mod != "" {
					modules[filepath.ToSlash(filepath.Dir(rel))] = mod
				}
				return nil
			}
			if d.Name() == "package.json" && !vendor {
				if name := readPackageName(path); name != "" {
					modules[filepath.ToSlash(filepath.Dir(rel))] = name
				}
				return nil
			}
			if lang.ForPath(rel) == nil {
				return nil
			}
			if !vendor && matcher.Match(strings.Split(rel, "/"), false) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			entries = append(entries, entry{
				rel:     rel,
				vendor:  vendor,
				size:    info.Size(),
				mtimeNs: info.ModTime().UnixNano(),
			})
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return entries, modules, nil
}

// readPackageName extracts the "name" field from a package.json.
func readPackageName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Name
}

// readModulePath extracts the module path from a go.mod file.
func readModulePath(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// buildMatcher combines the project's .gitignore hierarchy with extra
// exclude patterns from config. Gitignore read failures are not fatal:
// the index just gets a few extra files.
func buildMatcher(root string, cfg config.Config) gitignore.Matcher {
	var patterns []gitignore.Pattern
	if ps, err := gitignore.ReadPatterns(osfs.New(root), nil); err == nil {
		patterns = ps
	}
	for _, p := range cfg.Exclude {
		patterns = append(patterns, gitignore.ParsePattern(p, nil))
	}
	return gitignore.NewMatcher(patterns)
}

// inVendor reports whether any path component is a dependency dir.
func inVendor(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if config.VendorDirNames[part] {
			return true
		}
	}
	return false
}
