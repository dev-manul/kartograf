package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/enrich"
)

func newEnrichCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enrich",
		Short: "Add precision edges from type-inference tools",
		Long: `Runs a full type-inference pass and stores the resulting edges next to
the project in .kartograf/enrich.<source>.jsonl. The file is imported
into the index immediately and re-imported automatically by
index/serve whenever it changes — commit it or gitignore it as you
prefer.`,
	}
	cmd.AddCommand(newEnrichGoCmd(), newEnrichPHPCmd(), newImportCmd())
	return cmd
}

func openStoreFor(root, dbPath string) (*store.Store, string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	if dbPath == "" {
		if dbPath, err = store.DefaultPath(absRoot); err != nil {
			return nil, "", err
		}
	}
	s, err := store.Open(dbPath, absRoot)
	return s, absRoot, err
}

func logStderr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func newEnrichGoCmd() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "go [root]",
		Short: "Type-check Go modules (go/packages) and export precise call + implements edges",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			s, absRoot, err := openStoreFor(root, dbPath)
			if err != nil {
				return err
			}
			defer s.Close()

			edges, err := enrich.RunGo(absRoot, logStderr)
			if err != nil {
				return err
			}
			out := enrich.FilePath(absRoot, "go-types")
			if err := enrich.WriteFile(out, edges); err != nil {
				return err
			}
			n, err := enrich.ImportFile(s, absRoot, "go-types", out)
			if err != nil {
				return err
			}
			fmt.Printf("enriched: %d edges written to %s and imported\n", n, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the index database (default: user cache dir)")
	return cmd
}

func newEnrichPHPCmd() *cobra.Command {
	var (
		dbPath     string
		phpstanBin string
		neonPath   string
		memLimit   string
		skipRun    bool
	)
	cmd := &cobra.Command{
		Use:   "php [root]",
		Short: "Run PHPStan with the kartograf exporter rule and import the resulting edges",
		Long: `Scaffolds .kartograf/phpstan/ (exporter rule + neon config including the
project's own phpstan config), runs "php vendor/bin/phpstan analyse"
with it and imports the exported edges. If PHP is not available
locally, run phpstan wherever it works (e.g. in docker) with
KARTOGRAF_EDGES=<path> and the generated config, then import with
--skip-run.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			s, absRoot, err := openStoreFor(root, dbPath)
			if err != nil {
				return err
			}
			defer s.Close()

			if neonPath == "" {
				neonPath = enrich.FindProjectNeon(absRoot)
				if neonPath == "" {
					return fmt.Errorf("no phpstan.neon found at %s; pass --neon", absRoot)
				}
			}
			configPath, err := enrich.ScaffoldPHP(absRoot, neonPath)
			if err != nil {
				return err
			}
			out := enrich.FilePath(absRoot, "phpstan")

			if !skipRun {
				if phpstanBin == "" {
					phpstanBin = enrich.FindPHPStan(absRoot)
					if phpstanBin == "" {
						return fmt.Errorf("vendor/bin/phpstan not found under %s; pass --phpstan or use --skip-run", absRoot)
					}
				}
				logStderr("enrich(php): running phpstan (config %s)...", configPath)
				edges, err := enrich.RunPHPStan(absRoot, phpstanBin, configPath, memLimit, logStderr)
				if err != nil {
					return err
				}
				if err := enrich.WriteFile(out, edges); err != nil {
					return err
				}
			}

			n, err := enrich.ImportFile(s, absRoot, "phpstan", out)
			if err != nil {
				return err
			}
			fmt.Printf("enriched: %d edges in %s imported\n", n, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the index database (default: user cache dir)")
	cmd.Flags().StringVar(&phpstanBin, "phpstan", "", "path to the phpstan binary (default: autodetect vendor/bin/phpstan)")
	cmd.Flags().StringVar(&neonPath, "neon", "", "project phpstan config to include (default: autodetect)")
	cmd.Flags().StringVar(&memLimit, "memory-limit", "4G", "phpstan memory limit")
	cmd.Flags().BoolVar(&skipRun, "skip-run", false, "only scaffold config and import an existing JSONL")
	return cmd
}

func newImportCmd() *cobra.Command {
	var (
		dbPath string
		source string
	)
	cmd := &cobra.Command{
		Use:   "import <file.jsonl> [root]",
		Short: "Import an enrichment JSONL produced elsewhere (docker, another machine)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 2 {
				root = args[1]
			}
			s, absRoot, err := openStoreFor(root, dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			if source == "" {
				return fmt.Errorf("--source is required (e.g. phpstan, go-types)")
			}
			n, err := enrich.ImportFile(s, absRoot, source, args[0])
			if err != nil {
				return err
			}
			fmt.Printf("imported %d edges (source %s)\n", n, source)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the index database (default: user cache dir)")
	cmd.Flags().StringVar(&source, "source", "", "edge source label")
	return cmd
}
