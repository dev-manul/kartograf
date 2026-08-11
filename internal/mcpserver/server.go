// Package mcpserver exposes the code index to AI agents over the
// Model Context Protocol (stdio transport).
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dev-manul/kartograf/internal/core/query"
)

const defaultLimit = 50

// New builds the MCP server with all kartograf tools registered.
func New(q *query.Engine, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "kartograf",
		Title:   "Kartograf code map",
		Version: version,
	}, nil)
	register(s, q)
	return s
}

type searchIn struct {
	Query  string `json:"query" jsonschema:"free-text search over symbol names, FQNs and doc comments; multiple words are ANDed, each matched as a prefix"`
	Kind   string `json:"kind,omitempty" jsonschema:"optional filter: class, interface, trait, enum, enum_case, method, function, property, constant"`
	Name   string `json:"name,omitempty" jsonschema:"optional exact symbol name filter: name=create matches create() but not createUser()"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max results per page, default 50, max 500"`
	Offset int    `json:"offset,omitempty" jsonschema:"skip this many results (pagination; ordering is stable: project, then tests, then vendor, FTS rank within)"`
}

type fqnIn struct {
	FQN          string `json:"fqn" jsonschema:"symbol FQN, e.g. App\\Service\\Foo or App\\Service\\Foo::bar() or App\\Service\\Foo::$prop; a bare trailing segment like Foo::bar() also works"`
	Limit        int    `json:"limit,omitempty" jsonschema:"max results, default 50, max 500"`
	PathPrefix   string `json:"pathPrefix,omitempty" jsonschema:"only edges in files under this root-relative path prefix, e.g. api/src/"`
	ExcludeTests bool   `json:"excludeTests,omitempty" jsonschema:"drop edges located in test files/directories"`
}

func edgeFilter(in fqnIn) query.EdgeFilter {
	return query.EdgeFilter{PathPrefix: in.PathPrefix, ExcludeTests: in.ExcludeTests}
}

type getSymbolIn struct {
	FQN           string `json:"fqn" jsonschema:"symbol FQN; a bare trailing segment like UserService also works"`
	IncludeSource bool   `json:"includeSource,omitempty" jsonschema:"include the declaration source code (200-line window)"`
	SourceOffset  int    `json:"sourceOffset,omitempty" jsonschema:"start the source window this many lines below the declaration start — page through long bodies (the truncation marker names the next offset)"`
}

type outlineIn struct {
	Path string `json:"path" jsonschema:"project-root-relative file path with forward slashes"`
}

type symbolOut struct {
	query.SymbolHit
	Members []query.SymbolHit `json:"members,omitempty"`
	Source  string            `json:"source,omitempty"`
}

// Claude Code requires tool output schemas to be objects, so list
// results are wrapped.
type symbolsOut struct {
	Results []query.SymbolHit `json:"results"`
	// Total is the number of matches regardless of limit/offset;
	// Truncated signals that results is an incomplete page.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

type declarationsOut struct {
	Declarations []symbolOut `json:"declarations"`
}

type edgesOut struct {
	Results []query.EdgeHit `json:"results"`
}

type hierarchyOut struct {
	Ancestors   []query.Relative `json:"ancestors"`
	Descendants []query.Relative `json:"descendants"`
}

const maxLimit = 500

func limit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	default:
		return n
	}
}

// nonNil keeps empty results as [] (not null) in JSON output.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// methodSplitForImpact extracts the container FQN from a member FQN
// ("A\\B::m()" -> "A\\B", "pkg.T.M()" -> "pkg.T").
func methodSplitForImpact(fqn string) (string, string, bool) {
	if c, m, found := strings.Cut(fqn, "::"); found {
		return c, m, true
	}
	if strings.HasSuffix(fqn, "()") {
		if i := strings.LastIndex(fqn, "."); i >= 0 {
			return fqn[:i], fqn[i+1:], true
		}
	}
	return "", "", false
}

// resolveFQN expands a short name ("Foo::bar()", "Button") into the
// full FQN via the symbol table, so every graph tool honors the
// "a bare trailing segment also works" promise, not just get_symbol.
func resolveFQN(q *query.Engine, fqn string) string {
	if hits, err := q.GetSymbol(fqn); err == nil && len(hits) > 0 {
		return hits[0].FQN
	}
	return fqn
}

func register(s *mcp.Server, q *query.Engine) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_symbols",
		Description: "Search code symbols (classes, methods, functions, constants...) by name or doc text. " +
			"Args: query (free text), kind, name (exact symbol name), limit (max 500), offset. " +
			"Returns FQN, kind, file:line, signature, plus total/truncated for the full match count " +
			"(set limit=1 to just count). Project symbols rank before vendor ones.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, symbolsOut, error) {
		lim := limit(in.Limit)
		hits, total, err := q.SearchSymbols(in.Query, in.Kind, in.Name, lim, max(in.Offset, 0))
		return nil, symbolsOut{
			Results:   nonNil(hits),
			Total:     total,
			Truncated: in.Offset > 0 || len(hits) < total,
		}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_symbol",
		Description: "Get a symbol's declaration details. Args: fqn (full FQN or bare trailing segment " +
			"like UserService or Foo::bar()), includeSource (bool). Returns location, signature, doc, " +
			"members (for classes/interfaces/traits/enums) and optionally source code. To read a " +
			"method/function body before editing it, ONE call with includeSource=true is enough — " +
			"no extra file reads needed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in getSymbolIn) (*mcp.CallToolResult, declarationsOut, error) {
		var zero declarationsOut
		hits, err := q.GetSymbol(in.FQN)
		if err != nil {
			return nil, zero, err
		}
		if len(hits) == 0 {
			return nil, zero, fmt.Errorf("symbol %q not found; try search_symbols", in.FQN)
		}
		out := make([]symbolOut, 0, len(hits))
		for _, h := range hits {
			o := symbolOut{SymbolHit: h}
			switch h.Kind {
			case "class", "interface", "trait", "enum":
				if members, err := q.Members(h.FQN); err == nil {
					o.Members = members
				}
			}
			if in.IncludeSource {
				if src, err := q.Source(h, max(in.SourceOffset, 0), 200); err == nil {
					o.Source = src
				}
			}
			out = append(out, o)
		}
		return nil, declarationsOut{Declarations: out}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "find_references",
		Description: "Find all places referencing a symbol (calls, instantiations, type hints, instanceof, " +
			"constant access, inheritance). Args: fqn (or bare name), limit. Each edge carries " +
			"source (ast | phpstan | go-types) and resolved (false = heuristic match).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, edgesOut, error) {
		hits, err := q.References(resolveFQN(q, in.FQN), limit(in.Limit), edgeFilter(in))
		return nil, edgesOut{Results: nonNil(hits)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_callers",
		Description: "Who calls this method/function? Args: fqn (or bare name), limit. For methods the class " +
			"hierarchy is considered: calls via a parent interface/class reference are included and marked " +
			"resolved=false. Edges carry source (ast | phpstan | go-types). Optional pathPrefix/excludeTests filters.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, edgesOut, error) {
		hits, err := q.Callers(resolveFQN(q, in.FQN), limit(in.Limit), edgeFilter(in))
		return nil, edgesOut{Results: nonNil(hits)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_callees",
		Description: "What does this method/function call or instantiate? Lists outgoing call edges in source order.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, edgesOut, error) {
		hits, err := q.Callees(resolveFQN(q, in.FQN), limit(in.Limit), edgeFilter(in))
		return nil, edgesOut{Results: nonNil(hits)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "class_hierarchy",
		Description: "Full inheritance neighborhood of a class/interface/trait: transitive ancestors " +
			"(what it extends/implements/uses) and descendants (subclasses and implementations).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, hierarchyOut, error) {
		up, down, err := q.Hierarchy(resolveFQN(q, in.FQN))
		return nil, hierarchyOut{Ancestors: nonNil(up), Descendants: nonNil(down)}, err
	})

	type exploreIn struct {
		FQN        string `json:"fqn" jsonschema:"symbol FQN or bare trailing segment (UserService, Foo::bar())"`
		SkipSource bool   `json:"skipSource,omitempty" jsonschema:"omit source code of the declaration (saves tokens)"`
		MaxEdges   int    `json:"maxEdges,omitempty" jsonschema:"max callers and callees each, default 15"`
	}
	type exploreOut struct {
		Declarations    []symbolOut      `json:"declarations"`
		Callers         []query.EdgeHit  `json:"callers"`
		Callees         []query.EdgeHit  `json:"callees"`
		Ancestors       []query.Relative `json:"ancestors"`
		Descendants     []query.Relative `json:"descendants"`
		ReferencesTotal int              `json:"referencesTotal"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "explore",
		Description: "One-shot overview of a symbol: declaration with source, top callers and callees, " +
			"class hierarchy and total reference count in a single call. Args: fqn (or bare name), " +
			"skipSource, maxEdges. Start here for 'how does X work' questions; use the granular tools " +
			"(get_callers, find_references, ...) when you need one specific slice with filters.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in exploreIn) (*mcp.CallToolResult, exploreOut, error) {
		var out exploreOut
		fqn := resolveFQN(q, in.FQN)
		hits, err := q.GetSymbol(fqn)
		if err != nil {
			return nil, out, err
		}
		if len(hits) == 0 {
			return nil, out, fmt.Errorf("symbol %q not found; try search_symbols", in.FQN)
		}
		maxEdges := in.MaxEdges
		if maxEdges <= 0 || maxEdges > 100 {
			maxEdges = 15
		}
		for _, h := range hits[:min(len(hits), 3)] {
			o := symbolOut{SymbolHit: h}
			switch h.Kind {
			case "class", "interface", "trait", "enum":
				if members, err := q.Members(h.FQN); err == nil {
					o.Members = members
				}
			}
			if !in.SkipSource {
				if src, err := q.Source(h, 0, 120); err == nil {
					o.Source = src
				}
			}
			out.Declarations = append(out.Declarations, o)
		}
		if callers, err := q.Callers(fqn, maxEdges, query.EdgeFilter{}); err == nil {
			out.Callers = nonNil(callers)
		}
		if callees, err := q.Callees(fqn, maxEdges, query.EdgeFilter{}); err == nil {
			out.Callees = nonNil(callees)
		}
		if up, down, err := q.Hierarchy(fqn); err == nil {
			out.Ancestors = nonNil(up)
			if len(down) > 50 {
				down = down[:50]
			}
			out.Descendants = nonNil(down)
		}
		out.ReferencesTotal, _ = q.ReferencesCount(fqn)
		return nil, out, nil
	})

	type searchCodeIn struct {
		Query      string `json:"query" jsonschema:"substring (case-insensitive) or regex to find in project file contents"`
		Regex      bool   `json:"regex,omitempty" jsonschema:"treat query as a Go regular expression"`
		PathPrefix string `json:"pathPrefix,omitempty" jsonschema:"only files under this root-relative path prefix"`
		Limit      int    `json:"limit,omitempty" jsonschema:"max matching lines, default 50, max 200"`
	}
	type searchCodeOut struct {
		Results   []query.CodeMatch `json:"results"`
		Truncated bool              `json:"truncated"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_code",
		Description: "Full-text search over the CONTENTS of indexed project files (vendor excluded) — for " +
			"string literals, metric ids, SQL aliases, config keys and anything else that is not a declared " +
			"symbol. search_symbols only sees symbol names; use this when it returns nothing. " +
			"Args: query, regex, pathPrefix, limit.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchCodeIn) (*mcp.CallToolResult, searchCodeOut, error) {
		lim := in.Limit
		if lim <= 0 || lim > 200 {
			lim = 50
		}
		hits, truncated, err := q.SearchCode(in.Query, in.Regex, in.PathPrefix, lim)
		if err != nil {
			return nil, searchCodeOut{}, err
		}
		return nil, searchCodeOut{Results: nonNil(hits), Truncated: truncated}, nil
	})

	type impactIn struct {
		FQN      string `json:"fqn" jsonschema:"symbol FQN or bare trailing segment"`
		Depth    int    `json:"depth,omitempty" jsonschema:"how many caller levels to walk, default 3, max 5"`
		PerLevel int    `json:"perLevel,omitempty" jsonschema:"max callers reported per level, default 25"`
	}
	type impactOut struct {
		Levels        []query.ImpactLevel `json:"levels"`
		AffectedTests []string            `json:"affectedTests"`
		TestsNote     string              `json:"testsNote,omitempty"`
		Truncated     bool                `json:"truncated"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "impact",
		Description: "Blast radius of changing a symbol: transitive callers grouped by distance " +
			"(direct callers = depth 1, their callers = depth 2, ...) plus test files that exercise " +
			"the affected code. Args: fqn (or bare name), depth (default 3), perLevel (default 25). " +
			"Lower bound: only exactly-resolved call edges are followed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in impactIn) (*mcp.CallToolResult, impactOut, error) {
		var out impactOut
		depth := in.Depth
		if depth <= 0 || depth > 5 {
			depth = 3
		}
		perLevel := in.PerLevel
		if perLevel <= 0 || perLevel > 200 {
			perLevel = 25
		}
		fqn := resolveFQN(q, in.FQN)
		levels, tests, truncated, err := q.Impact(fqn, depth, perLevel)
		if err != nil {
			return nil, out, err
		}
		if len(tests) == 0 {
			// Test code sits outside the type-inference pass and mocks
			// defeat AST resolution, so direct call edges from tests
			// are rare — fall back to test files referencing the
			// symbol or its class in any way.
			targets := []string{fqn}
			if class, _, ok := methodSplitForImpact(fqn); ok {
				targets = append(targets, class)
			}
			if refs, err := q.TestFilesReferencing(targets, 20); err == nil && len(refs) > 0 {
				tests = refs
				out.TestsNote = "no call edges from tests reach this symbol; listed files reference the symbol or its class instead"
			} else {
				out.TestsNote = "no test references found in the graph — coverage may still exist via HTTP/acceptance tests"
			}
		}
		out.Levels = nonNil(levels)
		out.AffectedTests = nonNil(tests)
		out.Truncated = truncated
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "file_outline",
		Description: "List all symbols declared in a file. Args: path (project-root-relative, " +
			"forward slashes; not 'file').",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in outlineIn) (*mcp.CallToolResult, symbolsOut, error) {
		var zero symbolsOut
		hits, err := q.FileOutline(in.Path)
		if err == nil && len(hits) == 0 {
			return nil, zero, fmt.Errorf("no symbols for %q — check that the path is project-root-relative", in.Path)
		}
		return nil, symbolsOut{Results: nonNil(hits), Total: len(hits)}, err
	})
}
