package enrich

import (
	"testing"
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
		"api/src/Foo.php": true,
		"src/go-api/x.go": true,
	}
	root := "/Users/dev/project"
	cases := map[string]string{
		"/Users/dev/project/api/src/Foo.php": "api/src/Foo.php", // absolute under root
		"/app/api/src/Foo.php":               "api/src/Foo.php", // docker mount prefix
		"api/src/Foo.php":                    "api/src/Foo.php", // already relative
		"/somewhere/unknown.php":             "/somewhere/unknown.php",
	}
	for in, want := range cases {
		if got := normalizePath(in, root, indexed); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}
