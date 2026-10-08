package query

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/indexer"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/lang/ts"
)

func TestBarrelReexportsJoinCallers(t *testing.T) {
	ts.Register()
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
	write("lib/greet.ts", "export function greet() { return 'hi' }\n")
	write("lib/index.ts", "export { greet } from './greet'\n")
	write("app/main.ts", "import { greet } from '../lib'\nexport function main() { greet() }\n")

	write("icons/mark.ts", "export function mark() { return 1 }\n")
	write("icons/index.ts", "export { mark } from './mark'\n")
	write("kit/index.ts", "export * from '../icons'\n")
	write("app/use.ts", "import { mark } from '../kit'\nexport function use() { mark() }\n")

	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := indexer.Run(indexer.Options{Root: root, Store: s, Cfg: config.Default()}); err != nil {
		t.Fatal(err)
	}
	e := New(s, root)

	callers, err := e.Callers("lib/greet#greet()", 20, EdgeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].From != "app/main#main()" || callers[0].To != "lib/greet#greet()" || !callers[0].Resolved {
		t.Fatalf("named callers = %+v", callers)
	}
	callees, err := e.Callees("app/main#main()", 20, EdgeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(callees) != 1 || callees[0].To != "lib/greet#greet()" || !callees[0].Resolved {
		t.Fatalf("named callees = %+v", callees)
	}

	callers, err = e.Callers("icons/mark#mark()", 20, EdgeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].From != "app/use#use()" || callers[0].To != "icons/mark#mark()" || callers[0].Resolved {
		t.Fatalf("star callers = %+v", callers)
	}
	callees, err = e.Callees("app/use#use()", 20, EdgeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(callees) != 1 || callees[0].To != "icons/mark#mark()" || callees[0].Resolved {
		t.Fatalf("star callees = %+v", callees)
	}
}
