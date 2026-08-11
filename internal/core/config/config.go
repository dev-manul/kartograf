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
	return cfg, nil
}
