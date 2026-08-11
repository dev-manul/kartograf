package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/selfupdate"
)

func newSelfUpdateCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update kartograf to the latest GitHub release",
		Long: `Downloads the release asset for this OS/architecture, verifies its
SHA256 against the release's SHA256SUMS and atomically replaces the
current binary. Running MCP servers keep the old version until their
next restart.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return selfupdate.Run(version, checkOnly, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only check for a newer release, don't install")
	return cmd
}
