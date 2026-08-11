// Package selfupdate updates the running binary from GitHub releases:
// pick the asset for this OS/arch, verify its SHA256 against the
// release's SHA256SUMS, and atomically swap it in via rename (a fresh
// inode — replacing a signed binary in place would get the process
// SIGKILLed on macOS).
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	repo       = "dev-manul/kartograf"
	apiLatest  = "https://api.github.com/repos/" + repo + "/releases/latest"
	sumsAsset  = "SHA256SUMS"
	disableEnv = "KARTOGRAF_NO_UPDATE_CHECK"
)

// AssetName is the release asset for this platform.
func AssetName() string {
	return "kartograf-" + runtime.GOOS + "-" + runtime.GOARCH
}

type release struct {
	Tag    string
	Assets map[string]string // name -> download URL
}

func latestRelease(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiLatest, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	rel := &release{Tag: payload.TagName, Assets: map[string]string{}}
	for _, a := range payload.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

func download(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// expectedSum extracts the checksum for name from a SHA256SUMS body.
func expectedSum(sums, name string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			return fields[0]
		}
	}
	return ""
}

// Run checks the latest release and, unless checkOnly, replaces the
// current executable with it.
func Run(currentVersion string, checkOnly bool, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rel, err := latestRelease(ctx)
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	if rel.Tag == currentVersion {
		fmt.Fprintf(out, "kartograf %s is up to date\n", currentVersion)
		return nil
	}
	fmt.Fprintf(out, "current %s, latest %s\n", currentVersion, rel.Tag)
	if checkOnly {
		fmt.Fprintln(out, "run `kartograf self-update` to update")
		return nil
	}

	assetURL, ok := rel.Assets[AssetName()]
	if !ok {
		return fmt.Errorf("release %s has no asset %s", rel.Tag, AssetName())
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}

	// Download next to the target so the final rename stays on one
	// filesystem (atomic) and lands on a fresh inode.
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".kartograf-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	fmt.Fprintf(out, "downloading %s...\n", AssetName())
	if err := download(ctx, assetURL, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if sumsURL, ok := rel.Assets[sumsAsset]; ok {
		var sums strings.Builder
		if err := download(ctx, sumsURL, &sums); err != nil {
			return fmt.Errorf("download %s: %w", sumsAsset, err)
		}
		want := expectedSum(sums.String(), AssetName())
		if want == "" {
			return fmt.Errorf("%s has no entry for %s", sumsAsset, AssetName())
		}
		got, err := fileSHA256(tmpPath)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
		}
	} else {
		fmt.Fprintf(out, "warning: release has no %s, skipping verification\n", sumsAsset)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return err
	}
	fmt.Fprintf(out, "updated %s to %s\n", exe, rel.Tag)
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkState throttles background update checks to one per day.
type checkState struct {
	CheckedAt time.Time `json:"checkedAt"`
	LatestTag string    `json:"latestTag"`
}

func checkStatePath() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "kartograf", "update-check.json"), nil
}

// Notice returns a one-line upgrade hint when a newer release exists,
// checking GitHub at most once per day. Errors and the disable env
// var yield "". Meant for fire-and-forget use on serve startup.
func Notice(currentVersion string) string {
	if os.Getenv(disableEnv) != "" {
		return ""
	}
	path, err := checkStatePath()
	if err != nil {
		return ""
	}
	var st checkState
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if time.Since(st.CheckedAt) > 24*time.Hour {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rel, err := latestRelease(ctx)
		if err != nil {
			return ""
		}
		st = checkState{CheckedAt: time.Now(), LatestTag: rel.Tag}
		if data, err := json.Marshal(st); err == nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, data, 0o644)
		}
	}
	if st.LatestTag != "" && st.LatestTag != currentVersion {
		return fmt.Sprintf("kartograf %s is available (current %s) — run `kartograf self-update`",
			st.LatestTag, currentVersion)
	}
	return ""
}

// ErrUnsupported reserved for platforms without release assets.
var ErrUnsupported = errors.New("no release asset for this platform")
