package enrich

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/indexer"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/lang/php"
)

func TestParsePHPStanJSON(t *testing.T) {
	data := []byte(`{
		"totals": {"errors": 0, "file_errors": 3},
		"files": {
			"/app/api/src/Foo.php": {
				"errors": 3,
				"messages": [
					{"message": "{\"from\":\"App\\\\Foo::bar()\",\"kind\":\"calls\",\"to\":\"App\\\\Baz::qux()\"}", "line": 10, "identifier": "kartograf.edge"},
					{"message": "Some real phpstan finding", "line": 12, "identifier": "argument.type"},
					{"message": "{\"from\":\"App\\\\Foo::bar()\",\"kind\":\"instantiates\",\"to\":\"App\\\\Baz\"}", "line": 14, "identifier": "kartograf.edge"}
				]
			}
		},
		"errors": []
	}`)
	edges, err := parsePHPStanJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 {
		t.Fatalf("want 2 edges, got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.From != `App\Foo::bar()` || e.Kind != "calls" || e.To != `App\Baz::qux()` ||
		e.File != "/app/api/src/Foo.php" || e.Line != 10 {
		t.Errorf("edge[0] = %+v", e)
	}
}

func TestNormalizePath(t *testing.T) {
	indexed := map[string]bool{
		"api/src/Foo.php":                   true,
		"src/go-api/x.go":                   true,
		"domain-checker/internal/config.go": true,
		"metrics/internal/config.go":        true,
		"api/api/src/Bar.php":               true,
	}
	root := "/Users/dev/project"
	cases := []struct{ in, prefix, want string }{
		{"/Users/dev/project/api/src/Foo.php", "", "api/src/Foo.php"}, // absolute under root
		{"/app/api/src/Foo.php", "", "api/src/Foo.php"},               // docker mount prefix
		{"api/src/Foo.php", "", "api/src/Foo.php"},                    // already relative
		{"/somewhere/unknown.php", "", "/somewhere/unknown.php"},
		// Nested project: project-relative and container paths land in
		// the owning project even when a sibling has the same layout.
		{"internal/config.go", "domain-checker", "domain-checker/internal/config.go"},
		{"/app/internal/config.go", "metrics", "metrics/internal/config.go"},
		{"/Users/dev/project/metrics/internal/config.go", "domain-checker", "metrics/internal/config.go"},
		// api repo has an api/ subdirectory: project-relative api/src/Bar.php
		// is api/api/src/Bar.php from the workspace root.
		{"api/src/Bar.php", "api", "api/api/src/Bar.php"},
		{"/app/api/src/Bar.php", "api", "api/api/src/Bar.php"},
		// Unknown prefix still falls back to root-relative matching.
		{"api/src/Foo.php", "elsewhere", "api/src/Foo.php"},
	}
	for _, c := range cases {
		if got := normalizePath(c.in, root, c.prefix, indexed); got != c.want {
			t.Errorf("normalizePath(%q, prefix %q) = %q, want %q", c.in, c.prefix, got, c.want)
		}
	}
}

func TestExchangeFor(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "ws")
	cases := []struct {
		path           string
		prefix, origin string
	}{
		{filepath.Join(root, ".kartograf", "enrich.phpstan.jsonl"), "", ".kartograf/enrich.phpstan.jsonl"},
		{filepath.Join(root, "api", ".kartograf", "enrich.phpstan.jsonl"), "api", "api/.kartograf/enrich.phpstan.jsonl"},
		{filepath.Join(root, "a", "b", ".kartograf", "enrich.go-types.jsonl"), "a/b", "a/b/.kartograf/enrich.go-types.jsonl"},
		// Ad-hoc import from outside the root counts as the root's own.
		{filepath.Join(string(filepath.Separator), "tmp", "out.jsonl"), "", ".kartograf/enrich.phpstan.jsonl"},
	}
	for _, c := range cases {
		x := exchangeFor(root, "phpstan", c.path)
		if x.Prefix != c.prefix || x.Origin != c.origin {
			t.Errorf("exchangeFor(%q) = prefix %q origin %q, want %q / %q", c.path, x.Prefix, x.Origin, c.prefix, c.origin)
		}
	}
}

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

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".kartograf/enrich.go-types.jsonl", "")
	write(t, root, "api/.kartograf/enrich.phpstan.jsonl", "")
	write(t, root, "api/.kartograf/phpstan/kartograf.neon", "") // not an exchange file
	write(t, root, "svc/deep/.kartograf/enrich.phpstan.jsonl", "")
	write(t, root, "vendor/lib/.kartograf/enrich.phpstan.jsonl", "") // dependency dir
	write(t, root, "api/node_modules/x/.kartograf/enrich.go-types.jsonl", "")
	write(t, root, ".hidden/.kartograf/enrich.phpstan.jsonl", "")    // other dot-dir
	write(t, root, "clones/dup/.kartograf/enrich.phpstan.jsonl", "") // config exclude
	write(t, root, "ignored/.kartograf/enrich.phpstan.jsonl", "")    // .gitignore is NOT consulted
	write(t, root, ".gitignore", "/ignored/\n")

	cfg := config.Default()
	cfg.Exclude = []string{"clones/"}
	found, err := Discover(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var origins []string
	prefixes := map[string]string{}
	for _, x := range found {
		origins = append(origins, x.Origin)
		prefixes[x.Origin] = x.Prefix
	}
	want := []string{
		".kartograf/enrich.go-types.jsonl",
		"api/.kartograf/enrich.phpstan.jsonl",
		"ignored/.kartograf/enrich.phpstan.jsonl",
		"svc/deep/.kartograf/enrich.phpstan.jsonl",
	}
	if len(origins) != len(want) {
		t.Fatalf("origins = %v, want %v", origins, want)
	}
	for i := range want {
		if origins[i] != want[i] {
			t.Fatalf("origins = %v, want %v", origins, want)
		}
	}
	if prefixes["svc/deep/.kartograf/enrich.phpstan.jsonl"] != "svc/deep" || prefixes[".kartograf/enrich.go-types.jsonl"] != "" {
		t.Errorf("prefixes = %v", prefixes)
	}

	// include restricts the walk but keeps the root's own files.
	cfg.Include = []string{"svc"}
	found, err = Discover(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Origin != want[0] || found[1].Origin != want[3] {
		t.Errorf("with include: %+v", found)
	}
}

// Two nested PHP projects with identical layouts each ship a phpstan
// exchange file with container paths: edges must land in their own
// project, coexist in the store, and vanish when their file is deleted.
func TestAutoImportNestedProjects(t *testing.T) {
	php.Register()
	root := t.TempDir()
	write(t, root, "shop/src/Foo.php", "<?php\nnamespace Shop;\nclass Foo { public function a(): void {} }\n")
	write(t, root, "blog/src/Foo.php", "<?php\nnamespace Blog;\nclass Foo { public function a(): void {} }\n")
	write(t, root, "shop/.kartograf/enrich.phpstan.jsonl",
		`{"from":"Shop\\Foo::a()","kind":"calls","to":"Shop\\Bar::b()","file":"/app/src/Foo.php","line":3}`+"\n")
	write(t, root, "blog/.kartograf/enrich.phpstan.jsonl",
		`{"from":"Blog\\Foo::a()","kind":"calls","to":"Blog\\Bar::b()","file":"/app/src/Foo.php","line":3}`+"\n")

	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := config.Default()
	if _, err := indexer.Run(indexer.Options{Root: root, Store: s, Cfg: cfg}); err != nil {
		t.Fatal(err)
	}
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, format) }
	if err := AutoImport(s, root, cfg, logf); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 2 {
		t.Fatalf("expected 2 imports, got %d: %v", len(logged), logged)
	}

	files := func() map[string]string {
		rows, err := s.DB().Query(`SELECT from_fqn, file FROM ext_edges ORDER BY from_fqn`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var from, file string
			if err := rows.Scan(&from, &file); err != nil {
				t.Fatal(err)
			}
			out[from] = file
		}
		return out
	}
	got := files()
	if got[`Shop\Foo::a()`] != "shop/src/Foo.php" || got[`Blog\Foo::a()`] != "blog/src/Foo.php" || len(got) != 2 {
		t.Fatalf("edge files = %v", got)
	}
	stats, err := s.EnrichStats()
	if err != nil || stats["phpstan"] != 2 {
		t.Fatalf("stats = %v, err %v", stats, err)
	}

	// Unchanged files are not re-imported.
	logged = nil
	if err := AutoImport(s, root, cfg, logf); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 0 {
		t.Fatalf("unexpected re-import: %v", logged)
	}

	// Deleting one project's file drops only its edges.
	if err := os.Remove(filepath.Join(root, "blog/.kartograf/enrich.phpstan.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := AutoImport(s, root, cfg, logf); err != nil {
		t.Fatal(err)
	}
	got = files()
	if len(got) != 1 || got[`Shop\Foo::a()`] != "shop/src/Foo.php" {
		t.Fatalf("after delete: %v", got)
	}
}
