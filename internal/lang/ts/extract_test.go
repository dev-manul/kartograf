package ts

import (
	"os"
	"testing"

	"github.com/dev-manul/kartograf/internal/core/lang"
	"github.com/dev-manul/kartograf/internal/core/model"
)

// The fixture is extracted as if it lived at
// packages/web/src/components/Kitchen.tsx in a workspace where
// packages/ui-kit is published as @stripcash/ui-kit.
func extractFixture(t *testing.T) *model.FileIndex {
	t.Helper()
	src, err := os.ReadFile("testdata/kitchen_sink.tsx")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := New().ExtractFile("packages/web/src/components/Kitchen.tsx", src, lang.ExtractOptions{
		Modules: map[string]string{
			"packages/ui-kit": "@stripcash/ui-kit",
			"packages/web":    "web",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

const mod = "packages/web/src/components/Kitchen"

func TestTSSymbols(t *testing.T) {
	fi := extractFixture(t)
	if fi.HasErrors {
		t.Error("unexpected parse errors in fixture")
	}
	got := map[string]model.SymbolKind{}
	for _, s := range fi.Symbols {
		got[s.FQN] = s.Kind
	}
	want := map[string]model.SymbolKind{
		mod + "#MAX_RETRIES":                 model.KindConstant,
		mod + "#counter":                     model.KindProperty,
		mod + "#UserId":                      model.KindTypeAlias,
		mod + "#UserRepo":                    model.KindInterface,
		mod + "#UserRepo.find()":             model.KindMethod,
		mod + "#UserRepo.name":               model.KindProperty,
		mod + "#AdminRepo":                   model.KindInterface,
		mod + "#Status":                      model.KindEnum,
		mod + "#Status.Active":               model.KindEnumCase,
		mod + "#BaseService":                 model.KindClass,
		mod + "#UserService":                 model.KindClass,
		mod + "#UserService.repo":            model.KindProperty,
		mod + "#UserService.client":          model.KindProperty, // constructor param property
		mod + "#UserService.handleRefresh()": model.KindMethod,   // arrow-function field
		mod + "#UserService.constructor()":   model.KindMethod,
		mod + "#UserService.find()":          model.KindMethod,
		mod + "#formatUser()":                model.KindFunction,
		mod + "#Card()":                      model.KindFunction, // const arrow component
	}
	for fqn, kind := range want {
		if got[fqn] != kind {
			t.Errorf("symbol %s: kind = %q, want %q", fqn, got[fqn], kind)
		}
	}
}

func TestTSImports(t *testing.T) {
	fi := extractFixture(t)
	got := map[string]string{}
	for _, imp := range fi.Imports {
		got[imp.Alias] = imp.FQN
	}
	want := map[string]string{
		"React":  "react#default",
		"Button": "packages/ui-kit#Button", // package name mapped to workspace dir
		"UIText": "packages/ui-kit#Text",   // alias -> original exported name
		"api":    "packages/web/src/components/api/client",
		"helper": "packages/web/src/utils#helper", // ../utils relative to file dir
	}
	for alias, fqn := range want {
		if got[alias] != fqn {
			t.Errorf("import %s = %q, want %q", alias, got[alias], fqn)
		}
	}
}

func TestTSRefs(t *testing.T) {
	fi := extractFixture(t)
	got := map[string]bool{}
	for _, r := range fi.Refs {
		got[r.From+" "+string(r.Kind)+" "+r.To] = true
	}
	find := mod + "#UserService.find()"
	want := []string{
		// inheritance
		mod + "#AdminRepo extends " + mod + "#UserRepo",
		mod + "#UserService extends " + mod + "#BaseService",
		mod + "#UserService implements " + mod + "#UserRepo",
		// this., typed property hop, namespace import, named import
		find + " calls " + mod + "#UserService.log()",
		find + " calls " + mod + "#UserRepo.find()",
		find + " calls packages/web/src/components/api/client#fetchUser()",
		find + " calls packages/web/src/utils#helper()",
		find + " instantiates packages/ui-kit#Button",
		// typed parameter in a plain function
		mod + "#formatUser() calls " + mod + "#UserService.find()",
		// JSX component renders are calls, incl. through an alias
		mod + "#Card() calls packages/ui-kit#Button()",
		mod + "#Card() calls packages/ui-kit#Text()",
		// call inside a JSX prop closure attributes to the component
		mod + "#Card() calls packages/web/src/components/api/client#fetchUser()",
		// arrow-function field body
		mod + "#UserService.handleRefresh() calls " + mod + "#UserService.load()",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing ref %q", w)
		}
	}
	for key := range got {
		// <div> is not a component, globals are not resolvable.
		if key == mod+"#Card() references_type div" {
			t.Errorf("lowercase JSX element leaked: %s", key)
		}
	}
}

func TestModulePath(t *testing.T) {
	cases := map[string]string{
		"src/api/client.ts":     "src/api/client",
		"src/api/index.ts":      "src/api",
		"src/Card.tsx":          "src/Card",
		"src/types.d.ts":        "src/types",
		"lib/util.mjs":          "lib/util",
		"packages/x/index.d.ts": "packages/x",
	}
	for in, want := range cases {
		if got := modulePath(in); got != want {
			t.Errorf("modulePath(%q) = %q, want %q", in, got, want)
		}
	}
}
