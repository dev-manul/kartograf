package golang

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/dev-manul/kartograf/internal/core/model"
)

const maxSignatureLen = 200

// builtinFuncs never resolve to project symbols.
var builtinFuncs = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true,
	"complex": true, "copy": true, "delete": true, "imag": true,
	"len": true, "make": true, "max": true, "min": true, "new": true,
	"panic": true, "print": true, "println": true, "real": true,
	"recover": true,
}

// builtinTypes are predeclared type names that never denote a project
// symbol.
var builtinTypes = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"any": true, "comparable": true,
}

// extractor walks a parsed Go tree and fills a FileIndex.
type extractor struct {
	src      []byte
	fi       *model.FileIndex
	pkg      string // import path of the file's package
	skipRefs bool

	imports map[string]string // local name -> import path
	// fieldTypes: struct FQN -> field name -> field type FQN, for
	// resolving x.field.Method() one hop deep.
	fieldTypes map[string]map[string]string
	refSeen    map[model.Ref]bool
}

func (e *extractor) text(n *tree_sitter.Node) string { return n.Utf8Text(e.src) }

func nodeRange(n *tree_sitter.Node) model.Range {
	s, en := n.StartPosition(), n.EndPosition()
	return model.Range{
		StartLine: int(s.Row) + 1, StartCol: int(s.Column) + 1,
		EndLine: int(en.Row) + 1, EndCol: int(en.Column) + 1,
	}
}

func (e *extractor) addSymbol(s model.Symbol) {
	s.Lang = langID
	s.ID = langID + ":" + s.FQN
	s.File = e.fi.Path
	e.fi.Symbols = append(e.fi.Symbols, s)
}

func (e *extractor) addRef(r model.Ref) {
	if e.skipRefs && r.Kind != model.EdgeExtends {
		return
	}
	if r.To == "" || r.To == r.From {
		return
	}
	key := r
	key.Line = 0
	if e.refSeen[key] {
		return
	}
	e.refSeen[key] = true
	e.fi.Refs = append(e.fi.Refs, r)
}

// qual prefixes a name with the file's package path.
func (e *extractor) qual(name string) string {
	if e.pkg == "" {
		return name
	}
	return e.pkg + "." + name
}

// walkFile extracts in two passes: imports/types/values first so that
// struct field types are known before method bodies are walked,
// regardless of declaration order in the file.
func (e *extractor) walkFile(root *tree_sitter.Node) {
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		switch n.Kind() {
		case "import_declaration":
			e.extractImports(n)
		case "type_declaration":
			e.extractTypes(n)
		case "const_declaration", "var_declaration":
			e.extractValues(n)
		}
	}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		switch n.Kind() {
		case "function_declaration":
			e.extractFunction(n)
		case "method_declaration":
			e.extractMethod(n)
		}
	}
}

// extractImports handles both single and grouped import declarations.
func (e *extractor) extractImports(n *tree_sitter.Node) {
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n.Kind() == "import_spec" {
			pathNode := n.ChildByFieldName("path")
			if pathNode == nil {
				return
			}
			importPath := strings.Trim(e.text(pathNode), "`\"")
			alias := path_Base(importPath)
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				alias = e.text(nameNode) // includes "_" and "." forms
			}
			e.fi.Imports = append(e.fi.Imports, model.Import{Alias: alias, FQN: importPath})
			e.imports[alias] = importPath
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(n)
}

// path_Base is path.Base without importing path twice in this file.
func path_Base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (e *extractor) extractFunction(n *tree_sitter.Node) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := e.text(nameNode)
	fqn := e.qual(name) + "()"
	e.addSymbol(model.Symbol{
		Kind:      model.KindFunction,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(n),
		Signature: e.signature(n, n.ChildByFieldName("body")),
		Doc:       e.docComment(n),
	})
	vars := e.paramTypes(n)
	e.emitSignatureTypeRefs(n, fqn)
	if body := n.ChildByFieldName("body"); body != nil {
		e.walkBody(body, fqn, "", vars)
	}
}

func (e *extractor) extractMethod(n *tree_sitter.Node) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	recvType, recvVar := e.receiver(n)
	if recvType == "" {
		return
	}
	name := e.text(nameNode)
	container := e.qual(recvType)
	fqn := container + "." + name + "()"
	e.addSymbol(model.Symbol{
		Kind:      model.KindMethod,
		Name:      name,
		FQN:       fqn,
		Container: container,
		Range:     nodeRange(n),
		Signature: e.signature(n, n.ChildByFieldName("body")),
		Doc:       e.docComment(n),
	})
	vars := e.paramTypes(n)
	if recvVar != "" {
		vars[recvVar] = container
	}
	e.emitSignatureTypeRefs(n, fqn)
	if body := n.ChildByFieldName("body"); body != nil {
		e.walkBody(body, fqn, container, vars)
	}
}

// receiver returns the receiver's bare type name and variable name:
// `(s *UserService)` -> ("UserService", "s").
func (e *extractor) receiver(method *tree_sitter.Node) (typeName, varName string) {
	recv := method.ChildByFieldName("receiver")
	if recv == nil {
		return "", ""
	}
	for i := uint(0); i < recv.NamedChildCount(); i++ {
		p := recv.NamedChild(i)
		if p.Kind() != "parameter_declaration" {
			continue
		}
		if tn := p.ChildByFieldName("type"); tn != nil {
			typeName = e.bareTypeName(tn)
		}
		if nn := p.ChildByFieldName("name"); nn != nil {
			varName = e.text(nn)
		}
		break
	}
	return typeName, varName
}

// bareTypeName unwraps pointers/generics down to the base identifier:
// *Foo -> Foo, Foo[T] -> Foo. Returns "" for non-identifier types.
func (e *extractor) bareTypeName(n *tree_sitter.Node) string {
	for n != nil {
		switch n.Kind() {
		case "pointer_type":
			n = n.NamedChild(0)
		case "generic_type":
			n = n.ChildByFieldName("type")
		case "type_identifier":
			return e.text(n)
		default:
			return ""
		}
	}
	return ""
}

func (e *extractor) extractTypes(decl *tree_sitter.Node) {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		spec := decl.NamedChild(i)
		if spec.Kind() != "type_spec" && spec.Kind() != "type_alias" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		typeNode := spec.ChildByFieldName("type")
		if nameNode == nil {
			continue
		}
		name := e.text(nameNode)
		fqn := e.qual(name)

		kind := model.KindTypeAlias
		if typeNode != nil {
			switch typeNode.Kind() {
			case "struct_type":
				kind = model.KindClass
			case "interface_type":
				kind = model.KindInterface
			}
		}
		doc := e.docComment(spec)
		if doc == "" {
			doc = e.docComment(decl)
		}
		e.addSymbol(model.Symbol{
			Kind:      kind,
			Name:      name,
			FQN:       fqn,
			Range:     nodeRange(spec),
			Signature: e.typeSignature(spec, typeNode, kind),
			Doc:       doc,
		})
		if typeNode == nil {
			continue
		}
		switch typeNode.Kind() {
		case "struct_type":
			e.extractStructFields(typeNode, fqn)
		case "interface_type":
			e.extractInterfaceMembers(typeNode, fqn)
		default:
			e.emitTypeRefs(typeNode, fqn)
		}
	}
}

// typeSignature: for structs/interfaces the header only; otherwise the
// whole (usually short) definition.
func (e *extractor) typeSignature(spec, typeNode *tree_sitter.Node, kind model.SymbolKind) string {
	if typeNode != nil && (kind == model.KindClass || kind == model.KindInterface) {
		return "type " + e.signatureRange(spec.StartByte(), typeNode.StartByte()) + strings.TrimSuffix(typeNode.Kind(), "_type")
	}
	return "type " + e.signature(spec, nil)
}

func (e *extractor) extractStructFields(structType *tree_sitter.Node, containerFQN string) {
	list := structType.NamedChild(0)
	if list == nil || list.Kind() != "field_declaration_list" {
		return
	}
	for i := uint(0); i < list.NamedChildCount(); i++ {
		f := list.NamedChild(i)
		if f.Kind() != "field_declaration" {
			continue
		}
		typeNode := f.ChildByFieldName("type")
		var names []string
		for j := uint(0); j < f.NamedChildCount(); j++ {
			c := f.NamedChild(j)
			if c.Kind() == "field_identifier" {
				names = append(names, e.text(c))
			}
		}
		if len(names) == 0 && typeNode != nil {
			// Embedded field: hierarchy edge (Go composition ~ extends
			// for navigation purposes).
			if fqn, exact := e.resolveType(typeNode); fqn != "" {
				e.fi.Refs = append(e.fi.Refs, model.Ref{
					From: containerFQN, Kind: model.EdgeExtends, To: fqn,
					Resolved: exact, Line: nodeRange(f).StartLine,
				})
			}
			continue
		}
		e.emitTypeRefs(typeNode, containerFQN)
		if ft, _ := e.resolveType(typeNode); ft != "" {
			if e.fieldTypes[containerFQN] == nil {
				e.fieldTypes[containerFQN] = map[string]string{}
			}
			for _, name := range names {
				e.fieldTypes[containerFQN][name] = ft
			}
		}
		sig := e.signature(f, nil)
		for _, name := range names {
			e.addSymbol(model.Symbol{
				Kind:      model.KindProperty,
				Name:      name,
				FQN:       containerFQN + "." + name,
				Container: containerFQN,
				Range:     nodeRange(f),
				Signature: sig,
				Doc:       e.docComment(f),
			})
		}
	}
}

func (e *extractor) extractInterfaceMembers(ifaceType *tree_sitter.Node, containerFQN string) {
	for i := uint(0); i < ifaceType.NamedChildCount(); i++ {
		m := ifaceType.NamedChild(i)
		switch m.Kind() {
		case "method_elem", "method_spec": // grammar renamed over versions
			nameNode := m.ChildByFieldName("name")
			if nameNode == nil {
				continue
			}
			name := e.text(nameNode)
			e.addSymbol(model.Symbol{
				Kind:      model.KindMethod,
				Name:      name,
				FQN:       containerFQN + "." + name + "()",
				Container: containerFQN,
				Range:     nodeRange(m),
				Signature: e.signature(m, nil),
				Doc:       e.docComment(m),
			})
		case "type_elem": // embedded interface
			for j := uint(0); j < m.NamedChildCount(); j++ {
				if fqn, exact := e.resolveType(m.NamedChild(j)); fqn != "" {
					e.fi.Refs = append(e.fi.Refs, model.Ref{
						From: containerFQN, Kind: model.EdgeExtends, To: fqn,
						Resolved: exact, Line: nodeRange(m).StartLine,
					})
				}
			}
		}
	}
}

// extractValues handles const/var declarations (specs may declare
// several names).
func (e *extractor) extractValues(decl *tree_sitter.Node) {
	kind, prefix := model.KindConstant, "const "
	if decl.Kind() == "var_declaration" {
		kind, prefix = model.KindProperty, "var "
	}
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		spec := decl.NamedChild(i)
		if spec.Kind() != "const_spec" && spec.Kind() != "var_spec" {
			continue
		}
		sig := e.signature(spec, nil)
		doc := e.docComment(spec)
		if doc == "" {
			doc = e.docComment(decl)
		}
		for j := uint(0); j < spec.NamedChildCount(); j++ {
			c := spec.NamedChild(j)
			if c.Kind() != "identifier" {
				break // names come first; stop at type/value
			}
			name := e.text(c)
			if name == "_" {
				continue
			}
			e.addSymbol(model.Symbol{
				Kind:      kind,
				Name:      name,
				FQN:       e.qual(name),
				Range:     nodeRange(spec),
				Signature: prefix + sig,
				Doc:       doc,
			})
		}
	}
}

// paramTypes maps parameter names to resolved type FQNs for receiver
// call resolution in the body.
func (e *extractor) paramTypes(fn *tree_sitter.Node) map[string]string {
	vars := map[string]string{}
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return vars
	}
	for i := uint(0); i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		if p.Kind() != "parameter_declaration" {
			continue
		}
		typeNode := p.ChildByFieldName("type")
		fqn, _ := e.resolveType(typeNode)
		if fqn == "" {
			continue
		}
		for j := uint(0); j < p.NamedChildCount(); j++ {
			c := p.NamedChild(j)
			if c.Kind() == "identifier" {
				vars[e.text(c)] = fqn
			}
		}
	}
	return vars
}

// emitSignatureTypeRefs records references_type edges for parameter
// and result types of a function/method.
func (e *extractor) emitSignatureTypeRefs(fn *tree_sitter.Node, from string) {
	e.emitTypeRefs(fn.ChildByFieldName("parameters"), from)
	e.emitTypeRefs(fn.ChildByFieldName("result"), from)
}

// emitTypeRefs walks a type expression and records a references_type
// edge for every named type in it.
func (e *extractor) emitTypeRefs(n *tree_sitter.Node, from string) {
	if n == nil || e.skipRefs {
		return
	}
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "type_identifier", "qualified_type":
			if fqn, exact := e.resolveType(n); fqn != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeReferencesType, To: fqn, Resolved: exact, Line: nodeRange(n).StartLine})
			}
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(n)
}

// resolveType resolves a type expression to an FQN: local types get
// the file's package path, qualified ones their import's path.
// Builtins and type parameters yield "".
func (e *extractor) resolveType(n *tree_sitter.Node) (fqn string, exact bool) {
	for n != nil {
		switch n.Kind() {
		case "pointer_type", "parenthesized_type":
			n = n.NamedChild(0)
		case "generic_type":
			n = n.ChildByFieldName("type")
		case "qualified_type":
			pkgNode, nameNode := n.ChildByFieldName("package"), n.ChildByFieldName("name")
			if pkgNode == nil || nameNode == nil {
				return "", false
			}
			if imp, ok := e.imports[e.text(pkgNode)]; ok {
				return imp + "." + e.text(nameNode), true
			}
			return e.text(pkgNode) + "." + e.text(nameNode), false
		case "type_identifier":
			name := e.text(n)
			if builtinTypes[name] {
				return "", false
			}
			return e.qual(name), true
		default:
			return "", false
		}
	}
	return "", false
}

// walkBody collects call/instantiation references from executable code.
func (e *extractor) walkBody(n *tree_sitter.Node, from, container string, vars map[string]string) {
	if e.skipRefs {
		return
	}
	switch n.Kind() {
	case "call_expression":
		e.extractCall(n, from, container, vars)
	case "composite_literal":
		if tn := n.ChildByFieldName("type"); tn != nil {
			if fqn, exact := e.resolveType(tn); fqn != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeInstantiates, To: fqn, Resolved: exact, Line: nodeRange(n).StartLine})
			}
		}
	case "type_assertion_expression", "type_switch_statement":
		// x.(pkg.T) — type references inside are picked up below via
		// qualified_type/type_identifier children in emitTypeRefs form.
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		e.walkBody(n.NamedChild(i), from, container, vars)
	}
}

// extractCall records a calls edge for f(), pkg.F(), recv.M(),
// param.M() and method values.
func (e *extractor) extractCall(call *tree_sitter.Node, from, container string, vars map[string]string) {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return
	}
	line := nodeRange(call).StartLine
	switch fn.Kind() {
	case "identifier":
		name := e.text(fn)
		if builtinFuncs[name] {
			return
		}
		// Unqualified call: same-package function (or a local var —
		// heuristic).
		e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: e.qual(name) + "()", Resolved: false, Line: line})
	case "selector_expression":
		operand := fn.ChildByFieldName("operand")
		field := fn.ChildByFieldName("field")
		if operand == nil || field == nil {
			return
		}
		method := e.text(field)
		switch operand.Kind() {
		case "identifier":
			id := e.text(operand)
			if imp, ok := e.imports[id]; ok {
				// pkg.Func()
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: imp + "." + method + "()", Resolved: true, Line: line})
				return
			}
			if t, ok := vars[id]; ok {
				// receiver or typed parameter: r.Method()
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: t + "." + method + "()", Resolved: false, Line: line})
				return
			}
		case "selector_expression":
			// x.field.Method(): one hop through a struct field whose
			// type is declared in this file.
			innerOp := operand.ChildByFieldName("operand")
			innerField := operand.ChildByFieldName("field")
			if innerOp != nil && innerField != nil && innerOp.Kind() == "identifier" {
				if recvT, ok := vars[e.text(innerOp)]; ok {
					if ft, ok := e.fieldTypes[recvT][e.text(innerField)]; ok {
						e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: ft + "." + method + "()", Resolved: false, Line: line})
					}
				}
			}
		}
	}
}

func (e *extractor) signatureRange(start, end uint) string {
	if end > uint(len(e.src)) || start >= end {
		return ""
	}
	return strings.Join(strings.Fields(string(e.src[start:end])), " ") + " "
}

// signature returns the declaration text up to (not including) its
// body, whitespace-collapsed and capped.
func (e *extractor) signature(n *tree_sitter.Node, body *tree_sitter.Node) string {
	start, end := n.ByteRange()
	if body != nil {
		end, _ = body.ByteRange()
	}
	if end > uint(len(e.src)) || start >= end {
		return ""
	}
	sig := strings.Join(strings.Fields(string(e.src[start:end])), " ")
	if len(sig) > maxSignatureLen {
		sig = sig[:maxSignatureLen] + "…"
	}
	return sig
}

// docComment gathers the contiguous run of // comments directly above
// the declaration.
func (e *extractor) docComment(n *tree_sitter.Node) string {
	var parts []string
	expect := int(n.StartPosition().Row)
	for prev := n.PrevNamedSibling(); prev != nil && prev.Kind() == "comment"; prev = prev.PrevNamedSibling() {
		if int(prev.EndPosition().Row) != expect-1 && int(prev.EndPosition().Row) != expect {
			break
		}
		expect = int(prev.StartPosition().Row)
		parts = append([]string{e.text(prev)}, parts...)
	}
	return strings.Join(parts, "\n")
}
