// Why: each mk query kind turns a checked item into a few lines of Mk-like
// text; keeping those renderers apart from the lookup keeps query.go short.
//
// What: the answers sig, members, import, kind, shape, flows and errors, and
// the spellers they share: sigLine, declHead, flowClauses, perFieldClause,
// boundString and genericsString.
//
// How: every answer reads the checked tables (Signature, Facts, Layout) and
// spells them back in source syntax. query.go finds the subject and caps the
// answer.

package driver

import (
	"slices"
	"strings"

	"mk/internal/source"
	"mk/internal/types"
)

func (qx *queryIndex) str(sym source.Symbol) string { return qx.w.in.Str(sym) }

// funcSig is the signature of a function item, or nil.
func (qx *queryIndex) funcSig(id types.ItemID) *types.Signature {
	if d, ok := qx.w.items.Get(id).Decl.(*types.FuncDecl); ok {
		return &d.Sig
	}
	return nil
}

// boundString spells one bound.
func (qx *queryIndex) boundString(b types.Bound, gs []types.GenericParam) string {
	name := func(k types.BoundKind, item types.ItemID, args []types.GenericArg) string {
		switch k {
		case types.BoundPlain:
			return "Plain"
		case types.BoundSend:
			return "Send"
		case types.BoundShare:
			return "Share"
		}
		if item == types.NoItem {
			return "?"
		}
		s := qx.str(qx.w.items.Get(item).Name)
		if len(args) > 0 {
			var as []string
			for _, a := range args {
				as = append(as, qx.w.tab.GenericArgString(a, gs))
			}
			s += "[" + strings.Join(as, ", ") + "]"
		}
		return s
	}
	if b.Kind == types.BoundFields {
		return "Fields[" + name(b.Inner, b.Item, b.Args) + "]"
	}
	return name(b.Kind, b.Item, b.Args)
}

// genericsString spells [T: A + B, const N: int] for the given parameters.
func (qx *queryIndex) genericsString(gs []types.GenericParam, all []types.GenericParam) string {
	if len(gs) == 0 {
		return ""
	}
	var parts []string
	for _, g := range gs {
		p := qx.str(g.Name)
		if g.Const {
			p = "const " + p + ": " + qx.w.tab.TypeString(g.Type, all)
		} else if len(g.Bounds) > 0 {
			var bs []string
			for _, b := range g.Bounds {
				bs = append(bs, qx.boundString(b, all))
			}
			p += ": " + strings.Join(bs, " + ")
		}
		parts = append(parts, p)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// sigLine spells a signature as the reference writes one: prefix, own
// generics, parameters with into, result, then the flow clauses.
func (qx *queryIndex) sigLine(display string, s *types.Signature) string {
	tab := qx.w.tab
	var b strings.Builder
	if s.Exported {
		b.WriteString("export ")
	}
	if s.Extern {
		b.WriteString("extern ")
	}
	b.WriteString(display)
	own := s.Generics[len(s.Generics)-int(s.OwnGenerics):]
	b.WriteString(qx.genericsString(own, s.Generics))
	b.WriteByte('(')
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if i == 0 && s.Receiver != types.NoReceiver {
			b.WriteString([...]string{"", "self", "&self", "mut self"}[min(int(s.Receiver), 3)])
		} else {
			if p.Const {
				b.WriteString("const ")
			}
			if p.Var {
				b.WriteString("var ")
			}
			b.WriteString(qx.str(p.Name) + ": " + tab.TypeString(p.Type, s.Generics))
		}
		if p.Into != types.NoParam {
			b.WriteString(" into " + qx.paramName(s, p.Into))
		}
	}
	if s.Variadic {
		b.WriteString(", ...")
	}
	b.WriteByte(')')
	if r := tab.ReturnType(s.Result, s.Errors); r != types.Void {
		b.WriteString(": " + tab.TypeString(r, s.Generics))
	}
	if f := qx.flowClauses(s); f != "" {
		b.WriteString(" " + f)
	}
	return b.String()
}

func (qx *queryIndex) paramName(s *types.Signature, p types.ParamIndex) string {
	if p == types.NoParam || int(p) > len(s.Params) {
		return "?"
	}
	if int(p) == 1 && s.Receiver != types.NoReceiver {
		return "self"
	}
	return qx.str(s.Params[p-1].Name)
}

// fieldPath spells a Source's field steps from the parameter's type.
func (qx *queryIndex) fieldPath(s *types.Signature, src types.Source) string {
	if src.Param == types.NoParam || int(src.Param) > len(s.Params) {
		return ""
	}
	tab := qx.w.tab
	t := s.Params[src.Param-1].Type
	var out string
	for _, st := range src.Path {
		for tab.Kind(t) == types.KRef || tab.Kind(t) == types.KMut {
			t = tab.Get(t).Elem
		}
		name := "?"
		if ty := tab.Get(t); ty.Kind == types.KNamed {
			if d, ok := qx.w.items.Get(ty.Item).Decl.(*types.StructDecl); ok && int(st.Field) < len(d.Fields) {
				name = qx.str(d.Fields[st.Field].Name)
			}
		}
		out += "." + name
		t = st.Type
	}
	return out
}

// flowClauses spells from, *x into y and ends (only the declared ones are
// written in source; the automatic answer is shown by flows).
func (qx *queryIndex) flowClauses(s *types.Signature) string {
	var parts []string
	if s.Flows.Declared && len(s.Flows.Fields) > 0 {
		parts = append(parts, qx.perFieldClause(s))
	} else if s.Flows.Declared {
		var from []string
		for _, src := range s.Flows.Sources {
			from = append(from, qx.paramName(s, src.Param)+qx.fieldPath(s, src))
		}
		if len(from) > 0 {
			parts = append(parts, "from "+strings.Join(from, ", "))
		}
	}
	var st []string
	for _, x := range s.Flows.Stores {
		st = append(st, "*"+qx.paramName(s, x.From)+" into "+qx.paramName(s, x.Into))
	}
	if len(st) > 0 {
		parts = append(parts, strings.Join(st, ", "))
	}
	var ends []string
	for _, p := range s.Flows.Ends {
		ends = append(ends, qx.paramName(s, p))
	}
	if len(ends) > 0 {
		parts = append(parts, "ends "+strings.Join(ends, ", "))
	}
	return strings.Join(parts, " ")
}

// sig is the full signature, its contracts, and the item's /// doc lines.
func (qx *queryIndex) sig(e qEntry) []string {
	it := qx.w.items.Get(e.id)
	s := qx.funcSig(e.id)
	if s == nil {
		return append([]string{qx.declLine(e)}, qx.doc(it)...)
	}
	out := []string{qx.sigLine(e.name, s)}
	for _, c := range s.Contracts {
		w := "requires "
		if c.Kind == types.Ensures {
			w = "ensures "
		}
		out = append(out, "    "+w+c.Text)
	}
	out = append(out, "module "+e.mod+classSuffix(e.mod))
	return append(out, qx.doc(it)...)
}

// declLine spells a non-function item's head: kind, name, generics, and
// the word open of an open set.
func (qx *queryIndex) declLine(e qEntry) string { return qx.declHead(e, true) }

// declHead is declLine, with the word open when withOpen is set. mk apidiff
// compares the head without it and openness as a fact of its own, which a
// tree in the older layout marks with a doc line instead
// (apidiff_rules.go).
func (qx *queryIndex) declHead(e qEntry, withOpen bool) string {
	it := qx.w.items.Get(e.id)
	open := ""
	if withOpen && declOpen(it.Decl) {
		open = "open "
	}
	head := it.Kind.String() + " " + e.name
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		head = "struct " + e.name + qx.genericsString(d.Generics, d.Generics)
		if d.MutView {
			head = "mut view " + head
		} else if d.View {
			head = "view " + head
		}
		if d.Linear {
			head = "linear " + head
		}
	case *types.EnumDecl:
		head = open + "enum " + e.name + qx.genericsString(d.Generics, d.Generics)
	case *types.InterfaceDecl:
		head = "interface " + e.name + qx.genericsString(d.Generics, d.Generics)
	case *types.AliasDecl:
		head = open + "type " + e.name + qx.genericsString(d.Generics, d.Generics) + " = " + qx.w.tab.TypeString(d.Type, d.Generics)
	case *types.ConstDecl:
		head = "const " + e.name + ": " + qx.w.tab.TypeString(d.Type, nil)
		if d.HasValue {
			head += " = " + qx.w.tab.ConstString(d.Value)
		}
	case *types.GlobalDecl:
		head = "var " + e.name + ": " + qx.w.tab.TypeString(d.Type, nil)
	}
	if it.Exported {
		head = "export " + head
	}
	if c, _ := moduleClass(e.mod); c != ClassStable {
		return head + "  (module " + e.mod + ", " + c + ")"
	}
	return head + "  (module " + e.mod + ")"
}

// doc is the item's /// lines from its source, all of them: the answer's
// cap (QueryCapBytes) is the only cut, and it is counted in Truncated
// (a cut at a fixed number of lines would hide text and report
// "truncated": 0).
func (qx *queryIndex) doc(it *types.Item) []string {
	f := qx.w.sm.File(it.Span.File)
	if f == nil || int(it.Span.Start) > len(f.Text) {
		return nil
	}
	text := f.Text
	start := strings.LastIndexByte(text[:it.Span.Start], '\n') + 1
	var lines []string
	for start > 0 {
		prev := strings.LastIndexByte(text[:start-1], '\n') + 1
		l := strings.TrimSpace(text[prev : start-1])
		if !strings.HasPrefix(l, "///") {
			break
		}
		lines = append([]string{l}, lines...)
		start = prev
	}
	return lines
}

// variantLines is an enum's variants in declaration order, each with its
// payload: what members lists before the methods, and apidiff's "fields"
// fact for an enum, which holds no method.
func (qx *queryIndex) variantLines(d *types.EnumDecl) []string {
	var out []string
	for _, v := range d.Variants {
		l := qx.str(v.Name)
		if len(v.Fields) > 0 {
			var fs []string
			for _, f := range v.Fields {
				fs = append(fs, qx.str(f.Name)+": "+qx.w.tab.TypeString(f.Type, d.Generics))
			}
			l += "(" + strings.Join(fs, ", ") + ")"
		}
		out = append(out, l)
	}
	return out
}

// members lists fields (or variants) in declaration order, then the
// type's methods and impls, each with its signature; a std alias that
// re-exports a std-internal struct or enum (stdAliasTarget) lists its
// target's. A type whose parameters carry bounds or are const starts with
// its head, `type PriorityQueue[T: Ord]`, so the bounds are shown.
func (qx *queryIndex) members(e qEntry) []string {
	it := qx.w.items.Get(e.id)
	asked := e.name
	t, aliased := stdAliasTarget(qx.w, it)
	if aliased {
		// A std re-export (the prelude's Os over std.target's, or
		// std.time's Duration over std.core's): its target's members.
		e.id, it = t, qx.w.items.Get(t)
		e.name = qx.displayName(it)
	}
	tab := qx.w.tab
	var out []string
	head := func(gs []types.GenericParam) {
		for _, g := range gs {
			if g.Const || len(g.Bounds) > 0 {
				out = append(out, "type "+qx.str(it.Name)+qx.genericsString(gs, gs))
				return
			}
		}
	}
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		head(d.Generics)
		for _, f := range d.Fields {
			if e.std && !f.Exported && !qx.s.cfg.BrowseStd {
				continue // a std type's private fields are no member a user names
			}
			out = append(out, qx.fieldLine(f, d.Generics))
		}
	case *types.EnumDecl:
		head(d.Generics)
		out = append(out, qx.variantLines(d)...)
	case *types.ErrorDecl:
		for _, f := range d.Fields {
			out = append(out, qx.fieldLine(f, nil))
		}
	case *types.InterfaceDecl:
		for i := range d.Methods {
			out = append(out, qx.sigLine(qx.str(d.Methods[i].Name), &d.Methods[i]))
		}
		return out
	case *types.FuncDecl:
		return []string{e.name + " is a function, not a type: " + qx.sigLine(e.name, &d.Sig)}
	}
	// Methods: the Type.Name functions of the index whose owner is this
	// type, then impls for the type. The display name alone is not enough:
	// two modules may each declare a Pool.
	prefix := e.name + "."
	owner := e.id
	if t := qx.aliasTarget(e.id); t != types.NoItem {
		owner = t // an alias's methods are its target's (addAliasMethods)
	}
	for _, x := range qx.entries {
		if strings.HasPrefix(x.name, prefix) && !strings.Contains(x.name[len(prefix):], ".") && qx.w.items.Get(x.id).Owner == owner {
			if s := qx.funcSig(x.id); s != nil {
				out = append(out, qx.sigLine(lastSeg(x.name), s))
			}
		}
	}
	if aliased {
		// The target's module is std-internal, so the index holds none of
		// its methods (std.core's Duration.Nanos): its exported ones, in
		// key order.
		for _, id := range qx.w.items.Module(it.Module).Items {
			m := qx.w.items.Get(id)
			if m.Owner != e.id || m.Kind != types.KindFunc || !m.Exported && !qx.s.cfg.BrowseStd {
				continue
			}
			if s := qx.funcSig(id); s != nil && !slices.Contains(out, qx.sigLine(qx.str(m.Name), s)) {
				out = append(out, qx.sigLine(qx.str(m.Name), s))
			}
		}
	}
	for i := 1; i < qx.w.items.NumItems(); i++ {
		im, ok := qx.w.items.Get(types.ItemID(i)).Decl.(*types.ImplDecl)
		if !ok || im.For == types.NoType {
			continue
		}
		ty := tab.Get(im.For)
		switch {
		case ty.Kind == types.KNamed && ty.Item == e.id:
		case ty.Kind == types.KErrorSet && singleError(tab, ty.Errors) == e.id:
			// An impl for an error is keyed by the set of one.
		default:
			continue
		}
		line := "impl " + qx.boundString(im.Bound, im.Generics)
		if ty.Kind == types.KErrorSet && im.Bound.Item != types.NoItem && qx.w.items.Get(im.Bound.Item).Key == types.ItemKey("std.core", "Format") {
			// The compiler's Format, which no source declares.
			line += " for " + e.name + " (provided by the compiler)"
			out = append(out, line)
			for _, m := range im.Methods {
				if s := qx.funcSig(m); s != nil {
					out = append(out, "    "+qx.sigLine(qx.str(qx.w.items.Get(m).Name), s))
				}
			}
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		out = []string{asked + " has no members"}
	}
	return out
}

func (qx *queryIndex) fieldLine(f types.FieldDecl, gs []types.GenericParam) string {
	l := qx.str(f.Name) + ": " + qx.w.tab.TypeString(f.Type, gs)
	if f.Exported {
		l = "export " + l
	}
	return l
}

// importLine is the line that makes the name visible, and how code then
// spells it.
func (qx *queryIndex) importLine(e qEntry) []string {
	it := qx.w.items.Get(e.id)
	switch {
	case e.std && e.mod == preludeModule:
		return []string{e.name + " is in the prelude: no import", "spelled " + e.name}
	case !it.Exported && e.std:
		return []string{e.name + " is not exported"}
	}
	if e.module != nil && e.module.binRoot && !e.std && e.mod == "main" {
		return []string{e.name + " is in the root module: no import inside the package"}
	}
	out := []string{"import " + e.mod, "spelled " + lastSeg(e.mod) + "." + e.name}
	switch c, why := moduleClass(e.mod); c {
	case ClassExperimental:
		out = append(out, e.mod+" is Experimental: it may change or be withdrawn in any minor release; "+why)
	case ClassProvisional:
		out = append(out, e.mod+" is Provisional: it may change in a minor release, each change with a decision line")
	}
	return out
}

// shape answers kind T (with why) and shape T, for a non-generic struct,
// enum or alias; a generic type is answered by its kind rule only.
func (qx *queryIndex) shape(e qEntry, kindOnly bool) []string {
	it := qx.w.items.Get(e.id)
	var t types.TypeID
	generic := false
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		generic = len(d.Generics) > 0
		t = qx.w.tab.Named(e.id, nil)
	case *types.EnumDecl:
		generic = len(d.Generics) > 0
		t = qx.w.tab.Named(e.id, nil)
	case *types.AliasDecl:
		generic = len(d.Generics) > 0
		t = d.Type
	case *types.ErrorDecl:
		return []string{e.name + " is an error: plain, no references"}
	default:
		return []string{e.name + " is a " + it.Kind.String() + ", not a type"}
	}
	if generic {
		return []string{e.name + " is generic: its kind and shape follow its arguments (the maximum of its fields' kinds)",
			qx.declLine(e)}
	}
	chk := qx.w.sem5.chk
	f := chk.Facts(t)
	why := qx.kindWhy(it, f)
	if kindOnly {
		return append([]string{e.name + " is " + f.Kind.String()}, why...)
	}
	sh := chk.Shape(t)
	abi := ""
	if sh.ABI.Kind != types.AbiIgnore || sh.Size == 0 {
		abi = ", abi " + abiString(sh.ABI) // the shape pass leaves it zero when codegen computes it
	}
	out := []string{
		"size " + itoaQ(int(sh.Size)) + ", align " + itoaQ(int(sh.Align)) + abi,
		"kind " + sh.Kind.String() + ", carrier " + yes(sh.Carrier) + ", view " + yes(sh.View) + ", opaque " + yes(sh.Opaque),
		"Send " + yes(sh.Send) + ", Share " + yes(sh.Share),
	}
	return out
}

// kindWhy names the fields that make a struct unique or linear, or the word.
func (qx *queryIndex) kindWhy(it *types.Item, f types.Facts) []string {
	d, ok := it.Decl.(*types.StructDecl)
	if !ok || f.Kind == types.Plain {
		if f.Kind == types.Plain {
			return []string{"every part is plain"}
		}
		return nil
	}
	if d.Linear && f.Kind == types.Linear {
		return []string{"declared linear"}
	}
	var out []string
	for _, fd := range d.Fields {
		if k := qx.w.sem5.chk.Facts(fd.Type).Kind; k == f.Kind {
			out = append(out, "field "+qx.str(fd.Name)+": "+qx.w.tab.TypeString(fd.Type, d.Generics)+" is "+k.String())
		}
	}
	return out
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func abiString(a types.AbiClass) string {
	switch a.Kind {
	case types.AbiIgnore:
		return "ignore"
	case types.AbiIndirect:
		if a.Byval {
			return "indirect byval"
		}
		return "indirect"
	}
	var parts []string
	for i := 0; i < int(a.Count) && i < len(a.Parts); i++ {
		c := [...]string{"int", "float", "vector"}[min(int(a.Parts[i].Class), 2)]
		parts = append(parts, c+itoaQ(int(a.Parts[i].Bytes)*8))
	}
	return "direct " + strings.Join(parts, "+")
}

// flows answers the result's sources (declared or automatic), the stores
// and the ends.
func (qx *queryIndex) flows(e qEntry) []string {
	s := qx.funcSig(e.id)
	if s == nil {
		return []string{e.name + " is not a function"}
	}
	var out []string
	how := "automatic"
	if s.Flows.Declared {
		how = "declared"
	}
	if len(s.Flows.Sources) == 0 {
		out = append(out, "result holds no loan ("+how+")")
	}
	for _, src := range s.Flows.Sources {
		p := qx.paramName(s, src.Param) + qx.fieldPath(s, src)
		if src.Behind {
			out = append(out, "from "+p+": the result holds what "+p+" refers to; "+qx.paramName(s, src.Param)+" is free after the call ("+how+")")
		} else {
			out = append(out, "from "+p+": the result holds the loan; "+p+" stays borrowed ("+how+")")
		}
	}
	for _, x := range s.Flows.Stores {
		out = append(out, "*"+qx.paramName(s, x.From)+" into "+qx.paramName(s, x.Into))
	}
	for _, p := range s.Flows.Ends {
		out = append(out, "ends "+qx.paramName(s, p))
	}
	if len(s.Flows.Fields) > 0 {
		// The per-field clause as written; a field it leaves out holds
		// every source above.
		out = append(out, "per field: "+qx.perFieldClause(s))
	}
	if s.Flows.Ambiguous {
		out = append(out, "ambiguous: the automatic answer needs a from clause (E0601)")
	}
	return out
}

// perFieldClause spells a per-field clause as written, `from (f: a, g:
// b.c)`, the field names from the result's struct or record
// through ?T and E!T.
func (qx *queryIndex) perFieldClause(s *types.Signature) string {
	tab := qx.w.tab
	r := s.Result
	for tab.Kind(r) == types.KOptional || tab.Kind(r) == types.KErrorUnion {
		r = tab.Get(r).Elem
	}
	fieldName := func(i uint32) string {
		ty := tab.Get(r)
		switch ty.Kind {
		case types.KNamed:
			if d, ok := qx.w.items.Get(ty.Item).Decl.(*types.StructDecl); ok && int(i) < len(d.Fields) {
				return qx.str(d.Fields[i].Name)
			}
		case types.KRecord:
			if int(i) < len(ty.Fields) {
				return qx.str(ty.Fields[i].Name)
			}
		}
		return "?"
	}
	var fs []string
	for _, f := range s.Flows.Fields {
		var from []string
		for _, src := range f.Sources {
			from = append(from, qx.paramName(s, src.Param)+qx.fieldPath(s, src))
		}
		fs = append(fs, fieldName(f.Field)+": "+strings.Join(from, ", "))
	}
	return "from (" + strings.Join(fs, ", ") + ")"
}

// errors answers the error set, one member per line.
func (qx *queryIndex) errors(e qEntry) []string {
	s := qx.funcSig(e.id)
	if s == nil {
		return []string{e.name + " is not a function"}
	}
	ms := qx.w.tab.Members(s.Errors)
	if len(ms) == 0 {
		return []string{e.name + " cannot fail"}
	}
	var out []string
	for _, m := range ms {
		if m.IsParam {
			if int(m.Param) < len(s.Generics) {
				out = append(out, qx.str(s.Generics[m.Param].Name)+" (a generic error set)")
			}
			continue
		}
		it := qx.w.items.Get(m.Item)
		l := qx.str(it.Name) + "  " + qx.w.items.Module(it.Module).Path
		if d, ok := it.Decl.(*types.ErrorDecl); ok && len(d.Fields) > 0 {
			var fs []string
			for _, f := range d.Fields {
				fs = append(fs, qx.fieldLine(f, nil))
			}
			l += "  (" + strings.Join(fs, ", ") + ")"
		}
		out = append(out, l)
	}
	return out
}

// singleError is the error item of a set of one, NoItem otherwise. An impl
// of std.core.Format for such a set is the compiler's: a written
// one in the error's module is E0704, and in another module an orphan.
func singleError(tab *types.Table, set types.ErrorSetID) types.ItemID {
	ms := tab.Members(set)
	if len(ms) != 1 || ms[0].IsParam {
		return types.NoItem
	}
	return ms[0].Item
}

// providedFormatSig answers `sig E.Format` for an error E whose Format is
// the compiler's: the method's signature, then the impl noted as provided,
// with no source location.
func (qx *queryIndex) providedFormatSig(subject string) ([]string, bool) {
	i := strings.LastIndexByte(subject, '.')
	if i <= 0 || subject[i+1:] != "Format" {
		return nil, false
	}
	owner, name := subject[:i], "Format"
	e, ok := qx.lookup(owner)
	if !ok || qx.w.items.Get(e.id).Kind != types.KindError {
		return nil, false
	}
	tab := qx.w.tab
	for i := 1; i < qx.w.items.NumItems(); i++ {
		im, ok := qx.w.items.Get(types.ItemID(i)).Decl.(*types.ImplDecl)
		if !ok || im.For == types.NoType || im.Bound.Item == types.NoItem {
			continue
		}
		ty := tab.Get(im.For)
		if ty.Kind != types.KErrorSet || singleError(tab, ty.Errors) != e.id || qx.w.items.Get(im.Bound.Item).Key != types.ItemKey("std.core", "Format") {
			continue
		}
		for _, m := range im.Methods {
			if s := qx.funcSig(m); s != nil && qx.str(qx.w.items.Get(m).Name) == name {
				return []string{qx.sigLine(name, s), "impl Format for " + e.name + " (provided by the compiler)", "module " + e.mod}, true
			}
		}
	}
	return nil, false
}

// primMembers are the methods of a primitive type (str, i64, f64), which is
// no item: each std method whose owner is that primitive, in index order
// (so `members str` answers, rather than "no item named str").
func (qx *queryIndex) primMembers(subject string) []string {
	var out []string
	prefix := subject + "."
	for _, x := range qx.entries {
		it := qx.w.items.Get(x.id)
		if it.OwnerPrim == types.NoType || !strings.HasPrefix(x.name, prefix) {
			continue
		}
		if s := qx.funcSig(x.id); s != nil {
			out = append(out, qx.sigLine(lastSeg(x.name), s))
		}
	}
	return out
}
