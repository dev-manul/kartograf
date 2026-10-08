package query

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/indexer"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/lang/golang"
	"github.com/dev-manul/kartograf/internal/lang/php"
)

func TestProjectMap(t *testing.T) {
	php.Register()
	golang.Register()
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
	write("src/web.php", "<?php\nRoute::get('/pay', 'Pay');\nclass PayCommand extends Command {}\n$sql = \"SELECT id FROM users\";\n")
	write("cmd/root.go", "package cmd\nimport \"net/http\"\nfunc routes() {\n\thttp.HandleFunc(\"/health\", nil)\n}\nvar c = &cobra.Command{Use: \"reindex\"}\n")
	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := indexer.Run(indexer.Options{Root: root, Store: s, Cfg: config.Default()}); err != nil {
		t.Fatal(err)
	}
	got, err := New(s, root).ProjectMap(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) < 2 || len(got.Commands) < 2 || len(got.SQL) != 1 {
		t.Fatalf("map = %+v", got)
	}
}
