package php

import (
	"os"
	"strings"
	"testing"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/lang"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

func extractFixture(t *testing.T, name string) *model.FileIndex {
	t.Helper()
	src, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := New().ExtractFile("testdata/"+name, src, lang.ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func refSet(fi *model.FileIndex) map[string]bool {
	m := map[string]bool{}
	for _, r := range fi.Refs {
		m[r.From+" "+string(r.Kind)+" "+r.To] = true
	}
	return m
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

func TestKitchenSinkInheritance(t *testing.T) {
	fi := extractFixture(t, "kitchen_sink.php")
	got := refSet(fi)

	// Names are resolved with the file's namespace and use-map.
	want := []string{
		`App\Service\UserService extends App\Service\AbstractService`,
		`App\Service\UserService implements App\Contracts\RepositoryInterface`,
		`App\Service\UserService implements Countable`,
		`App\Service\UserService uses_trait App\Traits\LoggerAwareTrait`,
		`App\Service\UserService uses_trait App\Service\Cacheable`,
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing edge %q", w)
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

func TestRefsResolution(t *testing.T) {
	fi := extractFixture(t, "refs.php")
	if fi.HasErrors {
		t.Error("unexpected parse errors in fixture")
	}
	got := refSet(fi)
	run := `App\Service\UserService::run()`

	want := []string{
		// instantiation via use-map and alias
		run + ` instantiates App\Repo\UserRepository`,
		run + ` instantiates App\Events\UserCreated`,
		// static call, constant, ::class, static property
		run + ` calls App\Repo\UserRepository::create()`,
		run + ` references App\Repo\UserRepository::MAX_ROWS`,
		run + ` references_type App\Events\UserCreated`,
		run + ` references App\Service\Registry::$instances`,
		// $this / self / parent
		run + ` calls App\Service\UserService::helper()`,
		run + ` calls App\Service\BaseService::boot()`,
		// typed property (declared and promoted), typed parameter
		run + ` calls App\Repo\UserRepository::find()`,
		run + ` calls App\Service\Mailer::send()`,
		run + ` calls App\Service\Request::getBody()`,
		// functions: imported, global builtin, fully qualified
		run + ` calls App\Helpers\slug()`,
		run + ` calls strtolower()`,
		run + ` calls App\Helpers\other()`,
		// catch clause types
		run + ` references_type App\Service\NotFound`,
		run + ` references_type RuntimeException`,
		// signature type hints
		run + ` references_type App\Service\Request`,
		run + ` references_type App\Models\User`,
		// inheritance
		`App\Service\UserService extends App\Service\BaseService`,
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing ref %q", w)
		}
	}

	// Heuristic refs must be flagged as unresolved.
	for _, r := range fi.Refs {
		exactByRule := map[string]bool{
			run + " calls App\\Service\\BaseService::boot()":   false, // parent:: may climb higher
			run + " calls App\\Repo\\UserRepository::find()":   false, // property type heuristic
			run + " calls strtolower()":                        false, // ns fallback ambiguity
			run + " calls App\\Repo\\UserRepository::create()": true,
		}
		key := r.From + " " + string(r.Kind) + " " + r.To
		if wantExact, ok := exactByRule[key]; ok && r.Resolved != wantExact {
			t.Errorf("%s: resolved = %v, want %v", key, r.Resolved, wantExact)
		}
	}
}

func TestSkipRefs(t *testing.T) {
	src, err := os.ReadFile("testdata/refs.php")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := New().ExtractFile("testdata/refs.php", src, lang.ExtractOptions{SkipRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fi.Refs {
		switch r.Kind {
		case model.EdgeExtends, model.EdgeImplements, model.EdgeUsesTrait:
			// hierarchy facts are kept even in shallow mode
		default:
			t.Errorf("unexpected ref in shallow mode: %+v", r)
		}
	}
	if len(fi.Symbols) == 0 {
		t.Error("symbols must still be extracted in shallow mode")
	}
}
