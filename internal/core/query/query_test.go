package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/indexer"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/lang/php"
)

// buildEngine indexes a tiny project and returns an Engine over it.
func buildEngine(t *testing.T) *Engine {
	t.Helper()
	php.Register()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/Metrics.php", `<?php
namespace App;
class Metrics {
    public function register(): void {
        $ids = [
            'fraudSpammerAdd',
            'fraudAbuserAdd',
        ];
    }
    public function longBody(): void {
        $a = 1;
        $b = 2;
        $c = 3;
        $d = 4;
        $e = 5;
    }
}
`)
	write("tests/MetricsTest.php", `<?php
namespace Tests;
use App\Metrics;
class MetricsTest {
    public function testRegister(): void {
        $m = new Metrics();
        $m->register();
    }
}
`)
	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := indexer.Run(indexer.Options{Root: root, Store: s, Cfg: config.Default()}); err != nil {
		t.Fatal(err)
	}
	return New(s, root)
}

func TestSearchCode(t *testing.T) {
	e := buildEngine(t)

	hits, truncated, err := e.SearchCode("fraudspammeradd", false, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(hits) != 1 {
		t.Fatalf("literal search: %+v truncated=%v", hits, truncated)
	}
	if hits[0].File != "src/Metrics.php" || hits[0].Line != 6 || !strings.Contains(hits[0].Text, "fraudSpammerAdd") {
		t.Errorf("hit = %+v", hits[0])
	}

	hits, _, err = e.SearchCode(`fraud\w+Add`, true, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("regex search: want 2 hits, got %+v", hits)
	}

	hits, _, err = e.SearchCode("fraudSpammerAdd", false, "tests/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("pathPrefix filter leaked: %+v", hits)
	}

	// limit + truncation flag
	hits, truncated, err = e.SearchCode("fraud", false, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !truncated {
		t.Errorf("limit: hits=%d truncated=%v", len(hits), truncated)
	}
}

func TestSourceWindow(t *testing.T) {
	e := buildEngine(t)
	hits, err := e.GetSymbol(`App\Metrics::longBody()`)
	if err != nil || len(hits) != 1 {
		t.Fatalf("get symbol: %v %v", hits, err)
	}
	full, err := e.Source(hits[0], 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "$e = 5;") {
		t.Errorf("full source missing tail: %q", full)
	}

	win, err := e.Source(hits[0], 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(win, "sourceOffset=2") {
		t.Errorf("truncation hint should name the next offset, got %q", win)
	}
	next, err := e.Source(hits[0], 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "$b = 2;") {
		t.Errorf("offset window wrong: %q", next)
	}

	if _, err := e.Source(hits[0], 999, 2); err == nil {
		t.Error("offset past end should error with guidance")
	}
}

func TestTestFilesReferencing(t *testing.T) {
	e := buildEngine(t)
	files, err := e.TestFilesReferencing([]string{`App\Metrics`, `App\Metrics::register()`}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "tests/MetricsTest.php" {
		t.Errorf("test refs = %+v", files)
	}
}
