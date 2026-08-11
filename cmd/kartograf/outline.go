package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/core/lang"
	"github.com/dev-manul/kartograf/internal/core/model"
)

func newOutlineCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "outline <file>...",
		Short: "Print symbols declared in the given source files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, path := range args {
				fi, err := extractPath(path)
				if err != nil {
					return err
				}
				if asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					if err := enc.Encode(fi); err != nil {
						return err
					}
					continue
				}
				printOutline(fi)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full FileIndex as JSON")
	return cmd
}

func extractPath(path string) (*model.FileIndex, error) {
	adapter := lang.ForPath(path)
	if adapter == nil {
		return nil, fmt.Errorf("%s: no language adapter for this file type", path)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return adapter.ExtractFile(path, src, lang.ExtractOptions{})
}

func printOutline(fi *model.FileIndex) {
	suffix := ""
	if fi.HasErrors {
		suffix = "  [parse errors, best-effort]"
	}
	fmt.Printf("%s — %s, %d symbols%s\n", fi.Path, fi.Lang, len(fi.Symbols), suffix)

	rels := map[string][]string{}
	for _, r := range fi.Refs {
		switch r.Kind {
		case model.EdgeExtends, model.EdgeImplements, model.EdgeUsesTrait:
			rels[r.From] = append(rels[r.From], fmt.Sprintf("%s %s", r.Kind, r.To))
		}
	}

	for _, s := range fi.Symbols {
		indent := ""
		if s.Container != "" {
			indent = "  "
		}
		line := fmt.Sprintf("%s%-10s %s", indent, s.Kind, displayName(s))
		if sig := s.Signature; sig != "" && s.Kind != model.KindClass && s.Kind != model.KindInterface &&
			s.Kind != model.KindTrait && s.Kind != model.KindEnum {
			line = fmt.Sprintf("%s%-10s %s", indent, s.Kind, sig)
		}
		fmt.Printf("%s  :%d\n", line, s.Range.StartLine)
		if s.Container == "" {
			for _, r := range rels[s.FQN] {
				fmt.Printf("%s    %s\n", indent, r)
			}
		}
		if doc := docSummary(s.Doc); doc != "" {
			fmt.Printf("%s    ↳ %s\n", indent, doc)
		}
	}
}

// displayName shows the FQN for top-level symbols and the short name
// for members (their container is visible right above).
func displayName(s model.Symbol) string {
	if s.Container != "" {
		return s.Name
	}
	return s.FQN
}

// docSummary extracts the first meaningful line of a docblock.
func docSummary(doc string) string {
	for _, l := range strings.Split(doc, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "/*"))
		if l == "" || strings.HasPrefix(l, "@") {
			continue
		}
		return l
	}
	return ""
}
