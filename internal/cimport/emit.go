// Why: a C import must become ordinary Mk source that the front end checks
// like any module, readable by a person and written per target, so every
// C declaration needs a rule that maps it to Mk or says why it is skipped.
//
// What: the emitter, which maps the decoded declarations of the
// translation unit into one Mk module per import, `c.<name>`: types
// (mkType, fnType, paramType, argType), functions, enums, typedefs and
// extern vars, with Stats counting what was bound and skipped.
//
// How: C types map to Mk spellings by size and kind; a record with no Mk
// layout is usable only behind a pointer; a C function pointer becomes an
// optional fn type. Every skipped declaration is counted with its reason
// in Stats.

package cimport

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// emitter writes the bindings of one translation unit.
type emitter struct {
	u       *unit
	lay     map[string]*recLayout
	long64  bool // long is 64 bits (LP64)
	win     bool // the Windows x64 calling convention (aggregates by size, never by class)
	imports []Import
	macros  []*macro

	mkName map[*decl]string // records, enums and typedefs that are bound
	skip   map[*decl]string // why a declaration is not bound
	// ptrOnly are the records Mk can name only behind a pointer (Clang gave
	// no layout, or the alignment is no Mk integer's), with the reason: a
	// by-value use of one is not bound.
	ptrOnly map[*decl]string
	// sseRec are the records whose by-value passing Mk's replacement
	// cannot match on SysV, with the reason.
	sseRec  map[*decl]string
	scopes  []*nameScope
	mods    []*modOut
	stats   []Stats
	emitted map[*decl]bool
	shims   []shimFunc
	// vars is, by C name, the declaration that binds each extern variable
	// (varBinders): C may declare one variable more than once.
	vars map[string]*decl
	// funcLike are the function-like macros the headers define.
	funcLike map[string]bool
}

// modOut is one import's binding text being written.
type modOut struct {
	items   strings.Builder
	needRaw bool
	uses    map[int]bool
}

// shimFunc is one function the shim file defines under its C name.
type shimFunc struct {
	name  string
	ret   *ctype
	ps    []param
	macro bool // a function-like macro, not a static inline function
	imp   int  // the import whose binding declares it
}

// Stats counts one import's declarations.
type Stats struct {
	Decls     int            // declarations considered
	Direct    int            // bound as a plain Mk item
	Generated int            // bound through generated code: accessors or a shim
	Skipped   map[string]int // not bound, by reason
}

// Bound is Direct plus Generated: the declarations that import with no
// glue written by hand.
func (s Stats) Bound() int { return s.Direct + s.Generated }

func (e *emitter) skipped(imp int, reason string) {
	st := &e.stats[imp]
	if st.Skipped == nil {
		st.Skipped = map[string]int{}
	}
	st.Skipped[reason]++
}

// specialTypedefs are the typedefs a binding spells as Mk's pointer-sized
// integers.
var specialTypedefs = map[string]string{
	"size_t": "uint", "uintptr_t": "uint", "rsize_t": "uint",
	"ssize_t": "int", "ptrdiff_t": "int", "intptr_t": "int",
}

// builtin maps a builtin spelling to its Mk type and size.
func (e *emitter) builtin(name string) (string, int64, bool) {
	switch name {
	case "char", "unsigned char":
		return "u8", 1, true
	case "signed char":
		return "i8", 1, true
	case "short":
		return "i16", 2, true
	case "unsigned short":
		return "u16", 2, true
	case "int":
		return "i32", 4, true
	case "unsigned int":
		return "u32", 4, true
	case "long":
		if e.long64 {
			return "i64", 8, true
		}
		return "i32", 4, true
	case "unsigned long":
		if e.long64 {
			return "u64", 8, true
		}
		return "u32", 4, true
	case "long long":
		return "i64", 8, true
	case "unsigned long long":
		return "u64", 8, true
	case "float":
		return "f32", 4, true
	case "double":
		return "f64", 8, true
	case "_Bool":
		return "bool", 1, true
	case "void":
		return "void", 0, true
	}
	return "", 0, false
}

// resolve follows typedefs to the type t names.
func (e *emitter) resolve(t *ctype) *ctype {
	for i := 0; t != nil && t.kind == tTypedef && i < 64; i++ {
		d := e.u.typedefs[t.name]
		if d == nil || d.under == nil {
			return t
		}
		t = d.under
	}
	return t
}

// tagDecl is the declaration a record or enum type names.
func (e *emitter) tagDecl(t *ctype) *decl {
	if t.name == "" {
		return e.u.anon[t.anon]
	}
	kw := "struct "
	switch {
	case t.kind == tEnum:
		kw = "enum "
	case t.union:
		kw = "union "
	}
	return e.u.tags[kw+t.name]
}

// qual is d's Mk name as module imp spells it.
func (e *emitter) qual(d *decl, imp int) string {
	n := e.mkName[d]
	if d.imp != imp {
		e.mods[imp].uses[d.imp] = true
		return e.imports[d.imp].Name + "." + n
	}
	return n
}

// mkType spells C type t in Mk for module imp, or gives the reason it
// cannot. A pointer to a type that cannot be spelled is *void.
func (e *emitter) mkType(t *ctype, imp int) (string, string) {
	if t == nil {
		return "", "no type"
	}
	switch t.kind {
	case tBad:
		return "", "type"
	case tBuiltin:
		if m, _, ok := e.builtin(t.name); ok {
			return m, ""
		}
		return "", t.name
	case tTypedef:
		if m, ok := specialTypedefs[t.name]; ok {
			return m, ""
		}
		d := e.u.typedefs[t.name]
		if d == nil {
			return "", "builtin " + t.name
		}
		if n, ok := e.mkName[d]; ok && n != "" {
			return e.qual(d, imp), ""
		}
		if r, ok := e.skip[d]; ok {
			return "", r
		}
		return e.mkType(d.under, imp)
	case tRecord, tEnum:
		d := e.tagDecl(t)
		if d == nil {
			return "", "undeclared tag"
		}
		if d.kind == dEnum && e.mkName[d] == "" {
			return e.enumInt(d), ""
		}
		if _, ok := e.mkName[d]; !ok {
			if r, ok := e.skip[d]; ok {
				return "", r
			}
			return "", "unbound tag"
		}
		// A record Mk names only behind a pointer is no value type.
		if r, ok := e.ptrOnly[d]; ok {
			return "", r
		}
		return e.qual(d, imp), ""
	case tPointer:
		el := e.resolve(t.elem)
		if el != nil && el.kind == tFunc {
			// A C function pointer is an optional Mk fn type, since every
			// Mk function has the C ABI and ?fn is one pointer whose
			// null is none, so a field, result or variable binds and a
			// NULL reads as none. A variadic one, or one whose
			// parameters or result have no Mk spelling, stays *void. An
			// extern's own parameter is a plain fn type (argType).
			if s, why := e.fnType(el, imp); why == "" {
				return "?" + s, ""
			}
			return "*void", ""
		}
		if el != nil && el.kind == tBuiltin && el.name == "void" {
			return "*void", ""
		}
		if el != nil && el.kind == tRecord {
			if d := e.tagDecl(el); d != nil && e.ptrOnly[d] != "" && e.mkName[d] != "" {
				return "*" + e.qual(d, imp), ""
			}
		}
		s, r := e.mkType(t.elem, imp)
		if r != "" || s == "void" {
			return "*void", ""
		}
		return "*" + s, ""
	case tArray:
		if t.n < 0 {
			return "", "flexible array"
		}
		s, r := e.mkType(t.elem, imp)
		if r != "" {
			return "", r
		}
		return "[" + strconv.FormatInt(t.n, 10) + "]" + s, ""
	case tFunc:
		return e.fnType(t, imp)
	}
	return "", "type"
}

// fnType spells a function type as a Mk fn type; a variadic one cannot be.
func (e *emitter) fnType(t *ctype, imp int) (string, string) {
	if t.variadic {
		return "", "variadic function pointer"
	}
	var ps []string
	for _, p := range t.params {
		s, r := e.paramType(p, imp)
		if r == "" {
			r = e.byValue(p)
		}
		if r != "" {
			return "", r
		}
		ps = append(ps, s)
	}
	s := "fn(" + strings.Join(ps, ", ") + ")"
	rt, r := e.mkType(t.ret, imp)
	if r == "" {
		r = e.byValue(t.ret)
	}
	if r != "" {
		return "", r
	}
	if rt != "void" {
		s += ": " + rt
	}
	return s, ""
}

// paramType spells a parameter's type: an array or function parameter is
// a pointer (C11 6.7.6.3p7, p8).
func (e *emitter) paramType(t *ctype, imp int) (string, string) {
	r := e.resolve(t)
	if r != nil && r.kind == tArray {
		return e.mkType(&ctype{kind: tPointer, elem: r.elem}, imp)
	}
	if r != nil && r.kind == tFunc {
		return e.mkType(&ctype{kind: tPointer, elem: r}, imp)
	}
	s, why := e.mkType(t, imp)
	if why == "" && s == "void" {
		return "", "void parameter"
	}
	return s, why
}

// argType spells the type of an extern's own parameter: a C function
// pointer there is a Mk fn type, the one position where Mk hands C a
// function and never calls one C gave it. Its own parameters and
// result spell function pointers as *void (fnType, mkType), since C hands
// those to the Mk function.
func (e *emitter) argType(t *ctype, imp int) (string, string) {
	r := e.resolve(t)
	if r != nil && r.kind == tPointer {
		r = e.resolve(r.elem)
	}
	if r != nil && r.kind == tFunc && !r.variadic {
		if s, why := e.fnType(r, imp); why == "" {
			return s, ""
		}
	}
	return e.paramType(t, imp)
}

// byValue is why a parameter or result of type t cannot cross a call by
// value through the binding, "" when it can: on SysV, a record whose Mk
// form classifies otherwise than Clang's (sseRecord).
func (e *emitter) byValue(t *ctype) string {
	r := e.resolve(t)
	if r == nil || r.kind != tRecord {
		return ""
	}
	if d := e.tagDecl(r); d != nil {
		return e.sseRec[d]
	}
	return ""
}

// sizeAlign is t's size and alignment, or false when the binding cannot
// say.
func (e *emitter) sizeAlign(t *ctype) (int64, int64, bool) {
	t = e.resolve(t)
	if t == nil {
		return 0, 0, false
	}
	switch t.kind {
	case tBuiltin:
		if _, n, ok := e.builtin(t.name); ok && n > 0 {
			return n, n, true
		}
	case tTypedef:
		if m, ok := specialTypedefs[t.name]; ok && m != "" {
			return 8, 8, true
		}
	case tPointer:
		return 8, 8, true
	case tArray:
		if t.n < 0 {
			return 0, 0, false
		}
		s, a, ok := e.sizeAlign(t.elem)
		return s * t.n, a, ok
	case tRecord:
		d := e.tagDecl(t)
		if d == nil {
			return 0, 0, false
		}
		if l := e.lay[d.dumpKey]; l != nil {
			return l.size, l.align, true
		}
	case tEnum:
		d := e.tagDecl(t)
		if d == nil {
			return 4, 4, true
		}
		switch e.enumInt(d) {
		case "i64", "u64":
			return 8, 8, true
		}
		return 4, 4, true
	}
	return 0, 0, false
}

// enumInt is the integer type of an enum: its fixed type, else int, or
// unsigned int or a 64-bit type when the values need it (C23 6.7.2.2,
// Clang's rule for C).
func (e *emitter) enumInt(d *decl) string {
	if d.fixed != nil {
		if m, _ := e.mkType(d.fixed, d.imp); m != "" {
			return m
		}
	}
	switch {
	case d.enumMin >= -1<<31 && d.enumMax <= 1<<31-1:
		return "i32"
	case d.enumMin >= 0 && d.enumMax <= 1<<32-1:
		return "u32"
	case d.enumMin >= 0:
		return "u64"
	}
	return "i64"
}

// name assigns every Mk name, functions first so they keep their C names.
func (e *emitter) name() {
	for _, d := range e.u.decls {
		if d.kind != dFunc || e.u.funcs[d.name] != d {
			continue
		}
		if !e.scopes[d.imp].claim(d.name) {
			e.skip[d] = "function name"
		}
	}
	// Named tags and typedefs, in order.
	for _, d := range e.u.decls {
		switch d.kind {
		case dRecord:
			if d.name == "" || e.u.tags[d.dumpKey] != d {
				continue
			}
			suffix := "Struct"
			if d.union {
				suffix = "Union"
			}
			e.mkName[d] = e.scopes[d.imp].fresh(pascal(d.name), suffix)
		case dEnum:
			if d.name != "" && e.u.tags["enum "+d.name] == d {
				e.mkName[d] = e.scopes[d.imp].fresh(pascal(d.name), "Enum")
			}
		case dTypedef:
			if _, ok := specialTypedefs[d.name]; ok {
				e.mkName[d] = e.scopes[d.imp].fresh(pascal(d.name), "Type")
				continue
			}
			if e.u.typedefs[d.name] != d {
				continue
			}
			// typedef struct { } T: the record takes T's name.
			if o := e.u.byID[d.owned]; o != nil && o.name == "" && (o.kind == dRecord || o.kind == dEnum) {
				if _, done := e.mkName[o]; !done {
					e.mkName[o] = e.scopes[o.imp].fresh(pascal(d.name), "Type")
					e.mkName[d] = ""
					continue
				}
			}
			// typedef struct S S, or a typedef whose name folds like its
			// record's: one item.
			if r := e.resolve(d.under); r != nil && (r.kind == tRecord || r.kind == tEnum) {
				if td := e.tagDecl(r); td != nil && td.name != "" && fold(pascal(td.name)) == fold(pascal(d.name)) {
					e.mkName[d] = ""
					continue
				}
			}
			e.mkName[d] = e.scopes[d.imp].fresh(pascal(d.name), "Type")
		}
	}
	// Unnamed records named by the field that holds them.
	for _, d := range e.u.decls {
		if d.kind != dRecord || (d.name != "" && e.u.tags[d.dumpKey] != d) {
			continue
		}
		parent := e.mkName[d]
		k := 0
		for i := range d.fields {
			f := &d.fields[i]
			if f.anon == nil {
				continue
			}
			if _, done := e.mkName[f.anon]; done {
				continue
			}
			label := f.name
			if label == "" {
				label = "anon" + strconv.Itoa(k)
				k++
			}
			e.mkName[f.anon] = e.scopes[f.anon.imp].fresh(parent+pascal(label), "Type")
		}
	}
}

// Emit writes every binding. It returns one text per import.
func (e *emitter) emit() []string {
	e.pointerOnly()
	e.vars = e.varBinders()
	for _, d := range e.u.decls {
		if e.emitted[d] {
			continue
		}
		e.emitted[d] = true
		switch d.kind {
		case dFunc:
			if e.u.funcs[d.name] == d {
				e.function(d)
			}
		case dRecord:
			if d.name != "" && e.u.tags[d.dumpKey] != d {
				continue
			}
			e.record(d)
		case dEnum:
			e.enum(d)
		case dTypedef:
			e.typedef(d)
		case dVar:
			e.externVar(d)
		}
	}
	e.emitMacros()
	e.declaredMacros()
	out := make([]string, len(e.imports))
	for i, im := range e.imports {
		var b strings.Builder
		fmt.Fprintf(&b, "// The C import c.%s, generated by mk from its headers (see doc/reference.md).\n", im.Name)
		b.WriteString("// It is a view: edit pkg.mk's CImport, never this file.\n")
		fmt.Fprintf(&b, "// headers: %s\n", strings.Join(im.Headers, ", "))
		if len(im.Defines) > 0 {
			fmt.Fprintf(&b, "// defines: %s\n", strings.Join(im.Defines, ", "))
		}
		var uses []int
		for j := range e.mods[i].uses {
			uses = append(uses, j)
		}
		sort.Ints(uses)
		if e.mods[i].needRaw || len(uses) > 0 {
			b.WriteString("\n")
		}
		if e.mods[i].needRaw {
			b.WriteString("import std.raw\n")
		}
		for _, j := range uses {
			fmt.Fprintf(&b, "import c.%s\n", e.imports[j].Name)
		}
		b.WriteString(e.mods[i].items.String())
		out[i] = b.String()
	}
	return out
}

// function binds a function as an extern, or a static inline one as an
// extern its shim defines.
func (e *emitter) function(d *decl) {
	st := &e.stats[d.imp]
	st.Decls++
	if r, ok := e.skip[d]; ok {
		e.skipped(d.imp, r)
		return
	}
	if d.static && !d.inline {
		e.skipped(d.imp, "static function")
		return
	}
	if d.static && (!d.hasBody || !shimmable(d.ret, d.params) || d.variadic) {
		e.skipped(d.imp, "static inline function the shim cannot spell")
		return
	}
	sig, why := e.signature(d.name, d.ret, d.params, d.variadic, d.imp)
	if why != "" {
		e.skipped(d.imp, why)
		return
	}
	w := &e.mods[d.imp].items
	ft := &ctype{kind: tFunc, ret: d.ret, variadic: d.variadic}
	for _, p := range d.params {
		ft.params = append(ft.params, p.t)
	}
	spell, ok := cDecl(ft, d.name)
	if !ok {
		spell = d.name + " " + d.ctypeText
	}
	fmt.Fprintf(w, "\n// C: %s\n", spell)
	if d.static {
		fmt.Fprintf(w, "// static inline: defined by the shim of this package\n")
		e.shims = append(e.shims, shimFunc{name: d.name, ret: d.ret, ps: d.params, imp: d.imp})
		st.Generated++
	} else {
		st.Direct++
	}
	fmt.Fprintf(w, "export extern %s\n", sig)
}

// signature spells "name(params): R" in Mk, or gives the reason it cannot.
func (e *emitter) signature(name string, ret *ctype, ps []param, variadic bool, imp int) (string, string) {
	if variadic && len(ps) == 0 {
		return "", "variadic with no named parameter"
	}
	sc := newScope()
	var parts []string
	for i, p := range ps {
		t, why := e.argType(p.t, imp)
		if why == "" {
			why = e.byValue(p.t)
		}
		if why != "" {
			return "", why
		}
		n := p.name
		if n == "" {
			n = "p" + strconv.Itoa(i)
		}
		parts = append(parts, sc.fresh(snake(n), "_")+": "+t)
	}
	if variadic {
		parts = append(parts, "...")
	}
	s := name + "(" + strings.Join(parts, ", ") + ")"
	rt, why := e.mkType(ret, imp)
	if why == "" {
		why = e.byValue(ret)
	}
	if why != "" {
		return "", why
	}
	if rt != "void" {
		s += ": " + rt
	}
	return s, ""
}

// enum binds an enum as a type alias of its integer type and its
// enumerators as consts of it.
func (e *emitter) enum(d *decl) {
	st := &e.stats[d.imp]
	w := &e.mods[d.imp].items
	it := e.enumInt(d)
	typ := it
	if n := e.mkName[d]; n != "" {
		st.Decls++
		st.Direct++
		fmt.Fprintf(w, "\n// C: enum %s\nexport type %s = %s\n", d.name, n, it)
		typ = n
	}
	for _, c := range d.consts {
		st.Decls++
		n := e.scopes[d.imp].fresh(pascal(c.name), "Const")
		fmt.Fprintf(w, "// C: %s\nexport const %s: %s = %d\n", c.name, n, typ, c.value)
		st.Direct++
	}
}

// typedef binds a typedef as a type alias, unless its record took its
// name.
func (e *emitter) typedef(d *decl) {
	n, ok := e.mkName[d]
	if !ok || n == "" {
		return
	}
	st := &e.stats[d.imp]
	st.Decls++
	if m, ok := specialTypedefs[d.name]; ok {
		fmt.Fprintf(&e.mods[d.imp].items, "\n// C: typedef %s\nexport type %s = %s\n", d.name, n, m)
		st.Direct++
		return
	}
	t, why := e.mkType(d.under, d.imp)
	if why != "" || t == "void" {
		if why == "" {
			why = "void typedef"
		}
		delete(e.mkName, d)
		e.skip[d] = why
		e.skipped(d.imp, why)
		return
	}
	fmt.Fprintf(&e.mods[d.imp].items, "\n// C: typedef %s %s\nexport type %s = %s\n", d.under, d.name, n, t)
	st.Direct++
}

// externVar binds `extern T name;` as `extern var name: T`: readable and
// writable only in unsafe, as for any extern var; the binding's module is
// unsafe-permitted (the driver's cimport.go), and the name is the link symbol.
// A variable that is not extern (a definition in a header), or whose type has
// no Mk spelling or is a function or void, is skipped with its reason.
// C may declare an extern variable again (C11 6.2.2p4; the Windows SDK
// declares many IID_ names twice): it binds once, by the declaration
// varBinders picks, and counts as one declaration.
func (e *emitter) externVar(d *decl) {
	if !d.extern || d.vt == nil {
		e.stats[d.imp].Decls++
		e.skipped(d.imp, "variable that is not extern")
		return
	}
	if b := e.vars[d.name]; b != nil && b != d {
		return // another declaration binds the variable
	}
	e.stats[d.imp].Decls++
	s, why := e.mkType(d.vt, d.imp)
	if why == "" && (s == "void" || strings.HasPrefix(s, "fn(")) {
		why = "extern var of a function or void type"
	}
	if why != "" {
		e.skipped(d.imp, why)
		return
	}
	w := &e.mods[d.imp].items
	fmt.Fprintf(w, "\n// C: extern %s\n", d.name)
	fmt.Fprintf(w, "export extern var %s: %s\n", d.name, s)
	e.stats[d.imp].Direct++
}

// varBinders gives, by C name, the declaration of each extern variable
// that binds it, in its module and with its type: the first, while every
// later one has its type (typedefs followed); once one differs, the last.
// Clang accepts a redeclaration only with a compatible type, and gives each
// one the composite type of the declarations so far (C11 6.2.7): after
// `extern int z[];` and `extern int z[3];` the last is `int[3]`, where the
// first has no Mk spelling. The last is in the latest import, whose module
// may name the earlier imports' types.
func (e *emitter) varBinders() map[string]*decl {
	out := map[string]*decl{}
	first := map[string]*decl{}
	for _, d := range e.u.decls {
		if d.kind != dVar || !d.extern || d.vt == nil {
			continue
		}
		f := first[d.name]
		switch {
		case f == nil:
			first[d.name], out[d.name] = d, d
		case out[d.name] != f || !e.sameType(f.vt, d.vt):
			out[d.name] = d
		}
	}
	return out
}

// sameType reports whether C types a and b are one type once every
// typedef at every level is followed (qualifiers are already dropped):
// `extern const IID x` and `extern const GUID x` agree.
func (e *emitter) sameType(a, b *ctype) bool {
	a, b = e.resolve(a), e.resolve(b)
	if a == nil || b == nil {
		return a == b
	}
	if a.kind != b.kind || a.name != b.name || a.anon != b.anon || a.union != b.union {
		return false
	}
	switch a.kind {
	case tPointer:
		return e.sameType(a.elem, b.elem)
	case tArray:
		return a.n == b.n && e.sameType(a.elem, b.elem)
	case tFunc:
		if a.variadic != b.variadic || len(a.params) != len(b.params) || !e.sameType(a.ret, b.ret) {
			return false
		}
		for i := range a.params {
			if !e.sameType(a.params[i], b.params[i]) {
				return false
			}
		}
	}
	return true
}
