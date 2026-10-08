// Package config loads per-project kartograf configuration from
// .kartograf.yml at the project root. The config file is the only
// piece of kartograf state meant to be committed to the project repo;
// the index database itself is a derived artifact and lives in the
// user cache directory.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the config file looked up at the project root.
const FileName = ".kartograf.yml"

type Config struct {
	// Include lists root-relative directories to index.
	// Empty means the whole project root.
	Include []string `yaml:"include"`
	// Exclude lists extra gitignore-style patterns applied on top of
	// the project's .gitignore files.
	Exclude []string `yaml:"exclude"`
	// Vendor controls dependency directories (vendor/, node_modules/):
	// "index" (default) indexes them flagged as vendor code,
	// "skip" leaves them out entirely.
	Vendor string `yaml:"vendor"`
	// Task describes how a task id shows up in git branch names.
	// Used by find_task. An empty Branch matches any branch whose
	// name contains the id.
	Task Task `yaml:"task"`
	// Enrich tells kartograf where to obtain a graph that was built
	// on a machine that has PHP or Go.
	Enrich Enrich `yaml:"enrich"`
	// Stats is the optional price used to turn token estimates into money.
	Stats Stats `yaml:"stats"`
}

// Stats configures the local usage report. Nothing is uploaded.
type Stats struct {
	// DollarsPerMillion is the price of one million tokens the agent
	// spends reading source. Zero leaves the report in tokens only.
	DollarsPerMillion float64 `yaml:"dollars_per_million"`
}

// Enrich is the optional download of a prebuilt exchange file.
type Enrich struct {
	// URL is an https template containing "{commit}". When the local
	// PHPStan exchange file is missing or was built at another commit,
	// serve/index downloads it. Example:
	// https://example.com/kartograf/{commit}/enrich.phpstan.jsonl
	URL string `yaml:"url"`
}

// Task is the optional branch-name rule for one repository.
type Task struct {
	// Branch is a template containing "{id}", for example
	// "feature/{id}-". The id the user named replaces "{id}", and a
	// branch matches when its name contains the result.
	Branch string `yaml:"branch"`
}

// VendorDirNames are directory basenames treated as dependency roots.
var VendorDirNames = map[string]bool{
	"vendor":       true,
	"node_modules": true,
}

func Default() Config {
	return Config{Vendor: "index"}
}

// Load reads .kartograf.yml from root if present; a missing file
// yields the default config.
func Load(root string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", FileName, err)
	}
	if cfg.Vendor == "" {
		cfg.Vendor = "index"
	}
	if cfg.Vendor != "index" && cfg.Vendor != "skip" {
		return cfg, fmt.Errorf("%s: vendor must be \"index\" or \"skip\", got %q", FileName, cfg.Vendor)
	}
	if cfg.Task.Branch != "" && !strings.Contains(cfg.Task.Branch, "{id}") {
		return cfg, fmt.Errorf("%s: task.branch must contain {id}, got %q", FileName, cfg.Task.Branch)
	}
	if cfg.Enrich.URL != "" {
		if !strings.Contains(cfg.Enrich.URL, "{commit}") || !strings.HasPrefix(cfg.Enrich.URL, "https://") {
			return cfg, fmt.Errorf("%s: enrich.url must be an https URL containing {commit}, got %q", FileName, cfg.Enrich.URL)
		}
	}
	return cfg, nil
}
