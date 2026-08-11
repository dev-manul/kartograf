// Package mcpserver exposes the code index to AI agents over the
// Model Context Protocol (stdio transport).
package mcpserver

import (
	"context"
	"fmt"

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
	Query string `json:"query" jsonschema:"free-text search over symbol names, FQNs and doc comments; multiple words are ANDed, each matched as a prefix"`
	Kind  string `json:"kind,omitempty" jsonschema:"optional filter: class, interface, trait, enum, enum_case, method, function, property, constant"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results, default 50"`
}

type fqnIn struct {
	FQN   string `json:"fqn" jsonschema:"symbol FQN, e.g. App\\Service\\Foo or App\\Service\\Foo::bar() or App\\Service\\Foo::$prop; a bare trailing segment like Foo::bar() also works"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results, default 50"`
}

type getSymbolIn struct {
	FQN           string `json:"fqn" jsonschema:"symbol FQN; a bare trailing segment like UserService also works"`
	IncludeSource bool   `json:"includeSource,omitempty" jsonschema:"include the declaration source code (capped at 200 lines)"`
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
}

type declarationsOut struct {
	Declarations []symbolOut `json:"declarations"`
}

type edgesOut struct {
	Results []query.EdgeHit `json:"results"`
}

func limit(n int) int {
	if n <= 0 || n > 500 {
		return defaultLimit
	}
	return n
}

// nonNil keeps empty results as [] (not null) in JSON output.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func register(s *mcp.Server, q *query.Engine) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_symbols",
		Description: "Search code symbols (classes, methods, functions, constants...) by name or doc text. " +
			"Returns FQN, kind, file:line, signature. Project symbols rank before vendor ones.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, symbolsOut, error) {
		hits, err := q.SearchSymbols(in.Query, in.Kind, limit(in.Limit))
		return nil, symbolsOut{Results: nonNil(hits)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_symbol",
		Description: "Get a symbol's declaration details by FQN: location, signature, doc, members " +
			"(for classes/interfaces/traits/enums) and optionally its source code.",
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
				if src, err := q.Source(h, 200); err == nil {
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
			"constant access, inheritance). resolved=false rows are heuristic matches.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, edgesOut, error) {
		hits, err := q.References(in.FQN, limit(in.Limit))
		return nil, edgesOut{Results: nonNil(hits)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_callers",
		Description: "Who calls this method/function? For methods the class hierarchy is considered: calls via " +
			"a parent interface/class reference are included and marked resolved=false.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, edgesOut, error) {
		hits, err := q.Callers(in.FQN, limit(in.Limit))
		return nil, edgesOut{Results: nonNil(hits)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_callees",
		Description: "What does this method/function call or instantiate? Lists outgoing call edges in source order.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, edgesOut, error) {
		hits, err := q.Callees(in.FQN, limit(in.Limit))
		return nil, edgesOut{Results: nonNil(hits)}, err
	})

	type hierarchyOut struct {
		Ancestors   []query.Relative `json:"ancestors"`
		Descendants []query.Relative `json:"descendants"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "class_hierarchy",
		Description: "Full inheritance neighborhood of a class/interface/trait: transitive ancestors " +
			"(what it extends/implements/uses) and descendants (subclasses and implementations).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fqnIn) (*mcp.CallToolResult, hierarchyOut, error) {
		up, down, err := q.Hierarchy(in.FQN)
		return nil, hierarchyOut{Ancestors: nonNil(up), Descendants: nonNil(down)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "file_outline",
		Description: "List all symbols declared in a file (root-relative path).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in outlineIn) (*mcp.CallToolResult, symbolsOut, error) {
		var zero symbolsOut
		hits, err := q.FileOutline(in.Path)
		if err == nil && len(hits) == 0 {
			return nil, zero, fmt.Errorf("no symbols for %q — check that the path is project-root-relative", in.Path)
		}
		return nil, symbolsOut{Results: nonNil(hits)}, err
	})
}
