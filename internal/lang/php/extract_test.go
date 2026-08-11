package php

import (
	"os"
	"strings"
	"testing"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

func extractFixture(t *testing.T, name string) *model.FileIndex {
	t.Helper()
	src, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := New().ExtractFile("testdata/"+name, src)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func symbolsByFQN(fi *model.FileIndex) map[string]model.Symbol {
	m := make(map[string]model.Symbol, len(fi.Symbols))
	for _, s := range fi.Symbols {
		m[s.FQN] = s
	}
	return m
}

func TestKitchenSinkSymbols(t *testing.T) {
	fi := extractFixture(t, "kitchen_sink.php")
	if fi.HasErrors {
		t.Error("unexpected parse errors in fixture")
	}
	syms := symbolsByFQN(fi)

	want := map[string]model.SymbolKind{
		`App\Service\UserService`:                model.KindClass,
		`App\Service\UserService::PAGE_SIZE`:     model.KindConstant,
		`App\Service\UserService::STATUSES`:      model.KindConstant,
		`App\Service\UserService::$identityMap`:  model.KindProperty,
		`App\Service\UserService::$logger`:       model.KindProperty,
		`App\Service\UserService::$fallback`:     model.KindProperty,
		`App\Service\UserService::__construct()`: model.KindMethod,
		`App\Service\UserService::$cache`:        model.KindProperty,
		`App\Service\UserService::$repo`:         model.KindProperty,
		`App\Service\UserService::find()`:        model.KindMethod,
		`App\Service\UserService::hydrate()`:     model.KindMethod,
		`App\Service\UserService::count()`:       model.KindMethod,
		`App\Service\Flushable`:                  model.KindInterface,
		`App\Service\Flushable::flush()`:         model.KindMethod,
		`App\Service\Cacheable`:                  model.KindTrait,
		`App\Service\Cacheable::cacheKey()`:      model.KindMethod,
		`App\Service\Status`:                     model.KindEnum,
		`App\Service\Status::Active`:             model.KindEnumCase,
		`App\Service\Status::Banned`:             model.KindEnumCase,
		`App\Service\Status::label()`:            model.KindMethod,
		`App\Service\normalizeEmail()`:           model.KindFunction,
		`App\Service\GLOBAL_TTL`:                 model.KindConstant,
		`App\Legacy\OldService`:                  model.KindClass,
		`App\Legacy\OldService::run()`:           model.KindMethod,
	}
	for fqn, kind := range want {
		s, ok := syms[fqn]
		if !ok {
			t.Errorf("missing symbol %s", fqn)
			continue
		}
		if s.Kind != kind {
			t.Errorf("%s: kind = %s, want %s", fqn, s.Kind, kind)
		}
	}
}

func TestKitchenSinkImports(t *testing.T) {
	fi := extractFixture(t, "kitchen_sink.php")

	want := map[string]model.Import{
		"RepositoryInterface": {Alias: "RepositoryInterface", FQN: `App\Contracts\RepositoryInterface`},
		"Cache":               {Alias: "Cache", FQN: `App\Contracts\CacheInterface`},
		"User":                {Alias: "User", FQN: `App\Models\User`},
		"Logger":              {Alias: "Logger", FQN: `Psr\Log\LoggerInterface`},
		"normalize":           {Alias: "normalize", FQN: `App\Helpers\normalize`, Kind: "function"},
		"MAX_RETRIES":         {Alias: "MAX_RETRIES", FQN: `App\Helpers\MAX_RETRIES`, Kind: "const"},
	}
	got := map[string]model.Import{}
	for _, imp := range fi.Imports {
		got[imp.Alias] = imp
	}
	for alias, w := range want {
		g, ok := got[alias]
		if !ok {
			t.Errorf("missing import %s", alias)
			continue
		}
		if g != w {
			t.Errorf("import %s = %+v, want %+v", alias, g, w)
		}
	}
}

func TestKitchenSinkTypeRels(t *testing.T) {
	fi := extractFixture(t, "kitchen_sink.php")

	type rel struct {
		from string
		rel  model.EdgeKind
		to   string
	}
	want := []rel{
		{`App\Service\UserService`, model.EdgeExtends, "AbstractService"},
		{`App\Service\UserService`, model.EdgeImplements, "RepositoryInterface"},
		{`App\Service\UserService`, model.EdgeImplements, `\Countable`},
		{`App\Service\UserService`, model.EdgeUsesTrait, `\App\Traits\LoggerAwareTrait`},
		{`App\Service\UserService`, model.EdgeUsesTrait, "Cacheable"},
	}
	got := map[rel]bool{}
	for _, r := range fi.TypeRels {
		got[rel{r.From, r.Rel, r.To}] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing type relation %+v (have %+v)", w, fi.TypeRels)
		}
	}
}

func TestDocAndSignature(t *testing.T) {
	fi := extractFixture(t, "kitchen_sink.php")
	syms := symbolsByFQN(fi)

	find := syms[`App\Service\UserService::find()`]
	if find.Signature != "public function find(int $id): ?User" {
		t.Errorf("find() signature = %q", find.Signature)
	}
	if find.Doc == "" || !contains(find.Doc, "Finds a user by id.") {
		t.Errorf("find() doc = %q", find.Doc)
	}

	cls := syms[`App\Service\UserService`]
	if !contains(cls.Doc, "Handles user lifecycle operations.") {
		t.Errorf("class doc should survive the attribute in between, got %q", cls.Doc)
	}
	if !contains(cls.Signature, "final class UserService extends AbstractService") {
		t.Errorf("class signature = %q", cls.Signature)
	}

	abst := syms[`App\Service\UserService::hydrate()`]
	if !contains(abst.Signature, "abstract protected function hydrate(array $row): User") {
		t.Errorf("abstract method signature = %q", abst.Signature)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
