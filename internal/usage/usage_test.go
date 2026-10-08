package usage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordAndReport(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Record("get_symbol", 400, false, 4000)
	r.Record("get_callers", 100, true, 0)
	rep, err := r.Since(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Calls != 2 || rep.Empty != 1 || rep.Bytes != 500 || rep.Saved != 4000 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Tokens() != 125 || rep.SavedTokens() != 1000 {
		t.Fatalf("tokens = %d saved %d", rep.Tokens(), rep.SavedTokens())
	}
	text := Format(rep, 3)
	if !strings.Contains(text, "get_symbol 1") || !strings.Contains(text, "$0.0030") {
		t.Fatalf("format:\n%s", text)
	}
	line := r.SessionLine(3)
	if !strings.Contains(line, "2 calls") || !strings.Contains(line, "1 empty") {
		t.Fatalf("session = %q", line)
	}
}

func TestEstimateSaved(t *testing.T) {
	if EstimateSaved("search_symbols", 10, 1000) != 0 {
		t.Fatal("search is not a file read")
	}
	if EstimateSaved("get_callers", 100, 1000) != 900 {
		t.Fatal("expected file bytes minus response")
	}
	statsN, empty, files := ResultStats(map[string]any{
		"results": []any{map[string]any{"file": "a.go"}, map[string]any{"file": "a.go"}},
	})
	if empty || statsN == 0 || len(files) != 1 || files[0] != "a.go" {
		t.Fatalf("n=%d empty=%v files=%v", statsN, empty, files)
	}
	_, empty, _ = ResultStats(map[string]any{"results": []any{}})
	if !empty {
		t.Fatal("empty results")
	}
}
