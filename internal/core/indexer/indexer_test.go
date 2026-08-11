package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/lang/php"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalIndexing(t *testing.T) {
	php.Register()
	root := t.TempDir()

	write(t, root, "src/Foo.php", "<?php\nnamespace App;\nclass Foo { public function a(): void {} }\n")
	write(t, root, "src/Bar.php", "<?php\nnamespace App;\nclass Bar { public function b(): void {} }\n")
	write(t, root, "vendor/lib/Dep.php", "<?php\nnamespace Lib;\nclass Dep {}\n")
	write(t, root, "cache/Gen.php", "<?php class Generated {}\n")
	write(t, root, ".gitignore", "/cache/\n/vendor/\n")

	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	opts := Options{Root: root, Store: s, Cfg: config.Default()}

	// Fresh index: 2 project files + 1 vendor file; cache/ gitignored.
	stats, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 3 || stats.Unchanged != 0 || stats.Removed != 0 {
		t.Fatalf("fresh run: %+v", stats)
	}

	// No changes: everything unchanged.
	stats, err = Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 0 || stats.Unchanged != 3 {
		t.Fatalf("noop run: %+v", stats)
	}

	// mtime drift with identical content (branch switch and back):
	// stays unchanged, and the stored stat is refreshed.
	newTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "src/Foo.php"), newTime, newTime); err != nil {
		t.Fatal(err)
	}
	stats, err = Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 0 || stats.Unchanged != 3 {
		t.Fatalf("touch run: %+v", stats)
	}
	states, err := s.FileStates()
	if err != nil {
		t.Fatal(err)
	}
	if states["src/Foo.php"].MtimeNs != newTime.UnixNano() {
		t.Errorf("stored mtime not refreshed after content-equal change")
	}

	// Modify one file, delete another.
	write(t, root, "src/Foo.php", "<?php\nnamespace App;\nclass Foo { public function a2(): void {} }\n")
	if err := os.Remove(filepath.Join(root, "src/Bar.php")); err != nil {
		t.Fatal(err)
	}
	stats, err = Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 1 || stats.Unchanged != 1 || stats.Removed != 1 {
		t.Fatalf("incremental run: %+v", stats)
	}

	// Old symbol gone, new one present, Bar fully removed, FTS in sync.
	q := func(query string, args ...any) int {
		var n int
		if err := s.DB().QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := q(`SELECT COUNT(*) FROM symbols WHERE fqn = ?`, `App\Foo::a()`); n != 0 {
		t.Errorf("stale symbol a() still indexed")
	}
	if n := q(`SELECT COUNT(*) FROM symbols WHERE fqn = ?`, `App\Foo::a2()`); n != 1 {
		t.Errorf("new symbol a2() missing")
	}
	if n := q(`SELECT COUNT(*) FROM files WHERE path = ?`, "src/Bar.php"); n != 0 {
		t.Errorf("deleted file still in index")
	}
	if n := q(`SELECT COUNT(*) FROM symbols_fts WHERE symbols_fts MATCH 'a2'`); n != 1 {
		t.Errorf("FTS out of sync for new symbol")
	}
	if n := q(`SELECT COUNT(*) FROM symbols_fts WHERE symbols_fts MATCH 'Bar'`); n != 0 {
		t.Errorf("FTS out of sync for removed file")
	}
	if n := q(`SELECT COUNT(*) FROM files WHERE vendor = 1`); n != 1 {
		t.Errorf("vendor file not flagged")
	}
}

func TestVendorSkip(t *testing.T) {
	php.Register()
	root := t.TempDir()
	write(t, root, "src/Foo.php", "<?php class Foo {}\n")
	write(t, root, "vendor/lib/Dep.php", "<?php class Dep {}\n")

	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cfg := config.Default()
	cfg.Vendor = "skip"
	stats, err := Run(Options{Root: root, Store: s, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 1 {
		t.Fatalf("want only project file indexed, got %+v", stats)
	}
}
