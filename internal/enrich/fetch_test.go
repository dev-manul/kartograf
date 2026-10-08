package enrich

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-manul/kartograf/internal/core/config"
)

func TestDownloadRejectsPlainHTTP(t *testing.T) {
	if err := Download(filepath.Join(t.TempDir(), "out.jsonl"), "http://example.com/x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestMaybeFetchDownloadsMissingFile(t *testing.T) {
	root := gitRepo(t)
	head := HeadCommit(root)
	var gotUA string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		if !strings.Contains(r.URL.Path, head) {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("{\"kartograf\":\"enrich\",\"commit\":\"" + head + "\"}\n{\"from\":\"A\",\"kind\":\"calls\",\"to\":\"B\"}\n"))
	}))
	defer srv.Close()
	orig := newEnrichClient
	t.Cleanup(func() { newEnrichClient = orig })
	newEnrichClient = func() *http.Client { return srv.Client() }

	cfg := config.Config{Enrich: config.Enrich{URL: srv.URL + "/{commit}/enrich.phpstan.jsonl"}}
	MaybeFetch(root, cfg, func(string, ...any) {})
	data, err := os.ReadFile(FilePath(root, "phpstan"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), head) {
		t.Fatalf("body = %s", data)
	}
	if gotUA != "kartograf" {
		t.Fatalf("user agent = %q", gotUA)
	}

	// A file already stamped for HEAD is not downloaded again.
	gotUA = ""
	MaybeFetch(root, cfg, func(string, ...any) {})
	if gotUA != "" {
		t.Fatal("fetched over a current file")
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_EMAIL=kartograf@example.com", "GIT_AUTHOR_NAME=kartograf",
		"GIT_COMMITTER_EMAIL=kartograf@example.com", "GIT_COMMITTER_NAME=kartograf")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	return root
}
