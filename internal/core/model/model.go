// Package model defines the language-agnostic code model: symbols, edges
// and per-file indexes. Every language adapter normalizes its AST into
// these types; nothing here may be specific to a single language.
package model

// SymbolKind enumerates language-neutral symbol categories.
type SymbolKind string

const (
	KindClass     SymbolKind = "class"
	KindInterface SymbolKind = "interface"
	KindTrait     SymbolKind = "trait"
	KindEnum      SymbolKind = "enum"
	KindEnumCase  SymbolKind = "enum_case"
	KindMethod    SymbolKind = "method"
	KindFunction  SymbolKind = "function"
	KindProperty  SymbolKind = "property"
	KindConstant  SymbolKind = "constant"
	KindTypeAlias SymbolKind = "type_alias"
)

// EdgeKind enumerates language-neutral relations between symbols.
// Phase 1 only records extends/implements/uses_trait facts as raw names;
// resolution into edges happens in the resolve phase.
type EdgeKind string

const (
	EdgeCalls          EdgeKind = "calls"
	EdgeExtends        EdgeKind = "extends"
	EdgeImplements     EdgeKind = "implements"
	EdgeUsesTrait      EdgeKind = "uses_trait"
	EdgeInstantiates   EdgeKind = "instantiates"
	EdgeReferencesType EdgeKind = "references_type"
	EdgeImports        EdgeKind = "imports"
)

// Range is a location inside a file. Lines and columns are 1-based.
type Range struct {
	StartLine int `json:"startLine"`
	StartCol  int `json:"startCol"`
	EndLine   int `json:"endLine"`
	EndCol    int `json:"endCol"`
}

// Symbol is a single named declaration.
//
// ID is globally unique and deterministic: "<lang>:<FQN>", e.g.
// "php:App\Service\Foo::bar()". FQN formats are defined per language,
// but must be stable across re-indexing.
type Symbol struct {
	ID        string     `json:"id"`
	Lang      string     `json:"lang"`
	Kind      SymbolKind `json:"kind"`
	Name      string     `json:"name"`
	FQN       string     `json:"fqn"`
	Container string     `json:"container,omitempty"` // FQN of the enclosing symbol
	File      string     `json:"file"`
	Range     Range      `json:"range"`
	Signature string     `json:"signature,omitempty"`
	Doc       string     `json:"doc,omitempty"`
}

// Import is a name made available in a file's scope (PHP `use`,
// TS `import`, Go `import`).
type Import struct {
	Alias string `json:"alias"`          // local name in this file
	FQN   string `json:"fqn"`            // fully qualified target
	Kind  string `json:"kind,omitempty"` // "", "function", "const"
}

// TypeRel is an unresolved inheritance-like fact recorded during
// extraction: "class with FQN From <rel> name To" where To is written
// as it appears in source (resolved to an FQN later).
type TypeRel struct {
	From string   `json:"from"`
	Rel  EdgeKind `json:"rel"`
	To   string   `json:"to"`
}

// FileIndex is everything extracted from a single file.
type FileIndex struct {
	Path     string    `json:"path"`
	Lang     string    `json:"lang"`
	Symbols  []Symbol  `json:"symbols"`
	Imports  []Import  `json:"imports,omitempty"`
	TypeRels []TypeRel `json:"typeRels,omitempty"`
	// HasErrors reports that the parse tree contained ERROR nodes;
	// extraction is best-effort in that case.
	HasErrors bool `json:"hasErrors,omitempty"`
}
