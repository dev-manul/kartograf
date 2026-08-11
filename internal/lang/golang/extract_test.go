package golang

import (
	"os"
	"testing"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/lang"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

// The fixture is extracted as if it lived at src/go-api/services/svc
// inside a module named "go-api" rooted at src/go-api.
func extractFixture(t *testing.T) *model.FileIndex {
	t.Helper()
	src, err := os.ReadFile("testdata/kitchen_sink.go")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := New().ExtractFile("src/go-api/services/svc/kitchen_sink.go", src, lang.ExtractOptions{
		Modules: map[string]string{"src/go-api": "go-api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestGoSymbols(t *testing.T) {
	fi := extractFixture(t)
	if fi.HasErrors {
		t.Error("unexpected parse errors in fixture")
	}
	got := map[string]model.SymbolKind{}
	for _, s := range fi.Symbols {
		got[s.FQN] = s.Kind
	}
	want := map[string]model.SymbolKind{
		"go-api/services/svc.MaxRetries":         model.KindConstant,
		"go-api/services/svc.globalTimeout":      model.KindProperty,
		"go-api/services/svc.UserService":        model.KindClass,
		"go-api/services/svc.UserService.Repo":   model.KindProperty,
		"go-api/services/svc.UserService.name":   model.KindProperty,
		"go-api/services/svc.Finder":             model.KindInterface,
		"go-api/services/svc.Finder.Find()":      model.KindMethod,
		"go-api/services/svc.ID":                 model.KindTypeAlias,
		"go-api/services/svc.UserService.Find()": model.KindMethod,
		"go-api/services/svc.helper()":           model.KindFunction,
	}
	for fqn, kind := range want {
		if got[fqn] != kind {
			t.Errorf("symbol %s: kind = %q, want %q", fqn, got[fqn], kind)
		}
	}
}

func TestGoRefs(t *testing.T) {
	fi := extractFixture(t)
	got := map[string]bool{}
	for _, r := range fi.Refs {
		got[r.From+" "+string(r.Kind)+" "+r.To] = true
	}
	find := "go-api/services/svc.UserService.Find()"
	want := []string{
		// embedding as hierarchy
		"go-api/services/svc.UserService extends go-api/services/svc.BaseService",
		"go-api/services/svc.Finder extends fmt.Stringer",
		// field type through import alias
		"go-api/services/svc.UserService references_type go-api/services/userrepo.Repository",
		// calls: stdlib, aliased import, receiver, same package
		find + " calls fmt.Errorf()",
		find + " calls go-api/services/userrepo.New()",
		find + " calls go-api/services/svc.UserService.log()",
		find + " calls go-api/services/svc.helper()",
		// composite literals
		find + " instantiates go-api/services/svc.UserService",
		find + " instantiates go-api/services/svc.User",
		// signature types
		find + " references_type context.Context",
		find + " references_type go-api/services/svc.User",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing ref %q", w)
		}
	}
	for key := range got {
		if key == find+" calls len()" || key == find+" instantiates int" {
			t.Errorf("builtin leaked into refs: %s", key)
		}
	}
}

func TestGoDocAndSignature(t *testing.T) {
	fi := extractFixture(t)
	byFQN := map[string]model.Symbol{}
	for _, s := range fi.Symbols {
		byFQN[s.FQN] = s
	}
	find := byFQN["go-api/services/svc.UserService.Find()"]
	if find.Signature != "func (s *UserService) Find(ctx context.Context, id int) (*User, error)" {
		t.Errorf("Find() signature = %q", find.Signature)
	}
	if find.Doc != "// Find looks a user up." {
		t.Errorf("Find() doc = %q", find.Doc)
	}
	if c := byFQN["go-api/services/svc.MaxRetries"]; c.Doc != "// MaxRetries is the retry cap." {
		t.Errorf("const doc = %q", c.Doc)
	}
}

func TestPackagePath(t *testing.T) {
	mods := map[string]string{"src/go-api": "go-api", ".": "rootmod"}
	cases := map[string]string{
		"src/go-api/services/foo/a.go": "go-api/services/foo",
		"src/go-api/main.go":           "go-api",
		"cmd/tool/x.go":                "rootmod/cmd/tool",
		"top.go":                       "rootmod",
	}
	for path, want := range cases {
		if got := packagePath(path, mods); got != want {
			t.Errorf("packagePath(%q) = %q, want %q", path, got, want)
		}
	}
	if got := packagePath("a/b/c.go", map[string]string{}); got != "a/b" {
		t.Errorf("no-module fallback = %q, want a/b", got)
	}
}
