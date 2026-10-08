package enrich

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dev-manul/kartograf/internal/core/config"
)

const maxEnrichDownload = 256 << 20

// newEnrichClient is replaced in tests so a local TLS server can be trusted.
var newEnrichClient = func() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect left https")
			}
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// MaybeFetch downloads the PHPStan exchange file for HEAD when
// cfg.Enrich.URL is set and the local file is missing or was built at
// another commit. A local unstamped file is left alone. Failures are
// logged; indexing still runs on the AST layer.
func MaybeFetch(root string, cfg config.Config, logf func(format string, args ...any)) {
	if cfg.Enrich.URL == "" {
		return
	}
	head := HeadCommit(root)
	if head == "" {
		logf("enrich fetch: not a git checkout, skipped")
		return
	}
	dest := FilePath(root, "phpstan")
	stamp := fileCommit(dest)
	if _, err := os.Stat(dest); err == nil && (stamp == "" || stamp == head) {
		return
	}
	url := strings.ReplaceAll(cfg.Enrich.URL, "{commit}", head)
	if err := Download(dest, url); err != nil {
		logf("enrich fetch: %v", err)
		return
	}
	logf("enrich fetch: wrote %s for %s", dest, head[:min(12, len(head))])
}

// Download saves url to dest atomically. url must already be absolute https.
func Download(dest, url string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("enrich url must be https, got %q", url)
	}
	client := newEnrichClient()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "kartograf")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "enrich-*.jsonl")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxEnrichDownload+1))
	if err != nil {
		tmp.Close()
		return err
	}
	if n > maxEnrichDownload {
		tmp.Close()
		return fmt.Errorf("%s exceeds %d bytes", url, maxEnrichDownload)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func fileCommit(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	var stamp enrichStamp
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &stamp); err != nil {
		return ""
	}
	if stamp.Kartograf != "enrich" {
		return ""
	}
	return stamp.Commit
}
