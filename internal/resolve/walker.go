// Why: within one item, names come and go with blocks, loops, generics and
// fn literals; the walker carries those scopes so every lookup sees exactly
// the names in scope at its point.
//
// What: walker (one task's walk state), the frames scoped, bodyFrame,
// litFrame and fieldsFrame, the local declaring methods (declare,
// declareLocal, declareParam, declareSelf, pushTypeParams), shadowsConstParam,
// recaseLocal and renameFix, literal and closeCaptures, and unusedLocals (E0208).
//
// How: locals live on one stack, with a frame per body; a new local is
// compared, folded, with every visible local of this body and the
// enclosing ones, and exactly with the const generics in scope (E0206). A
// fn literal is lifted to its own item; its captures are collected per
// frame in source order, then sorted by name.

package resolve

import (
	"sort"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// walker resolves the nodes of one task. It writes only the task's slot,
// its items' ItemInfo and the side-table slots of the nodes it walks.
type walker struct {
	m  *Module
	t  *task
	fs *fileState
	// rec is the item whose reads and literals the walk is recording.
	rec    *itemRec
	bodies []*bodyFrame
	locals []scoped
	labels []label
	// tparams are the generics in scope, outermost declaration first.
	tparams []genParam
	// fields are a struct's fields inside an invariant (RefField), and
	// fieldsBody the index in bodies of the invariant's body: a literal
	// above it that names a field captures the invariant's self (useField).
	fields     []fieldName
	fieldsBody int
	// result is the function whose ensures clause is being walked, and
	// resultBody the index in bodies of the predicate's body: a literal
	// above it captures result (useResult).
	result     types.ItemID
	resultBody int
	// selfType is the interface or impl whose Self is in scope.
	selfType types.ItemID
	lits     []*litFrame
	// fieldsLoops are the fields loops being walked (for FieldsLoop).
	fieldsLoops []fieldsFrame
	causes      map[causeKey]source.Span
	// destr is set while a destructure's labeled sub-patterns are walked.
	destr *destrCtx
	// origin is the NameOrigin of the token being resolved in generated
	// code; zero elsewhere.
	origin NameOrigin
}

// scoped is one local on the stack.
type scoped struct {
	sym   source.Symbol
	fold  string
	local LocalID
	body  int
	at    source.Span
	// constFn: a const parameter whose declared type is a fn type.
	constFn bool
}

// fieldsFrame is one fields loop being walked: the length of locals when its
// head began, and the body (index into walker.bodies) whose statement it
// is. Only that body is checked once per field; a literal inside
// the loop is a unit of its own, checked once.
type fieldsFrame struct {
	locals int
	body   int
}

type label struct {
	sym  source.Symbol
	node ast.NodeID
	body int
	at   source.Span
}

type fieldName struct {
	sym  source.Symbol
	idx  int
	item types.ItemID
	at   source.Span
}

// bodyFrame is one body being walked: the item's own, or a literal's.
type bodyFrame struct {
	rec  *itemRec
	body *Body
	// binds holds, per local, the statement that declared it, and at the
	// span of its name (E0208 and its fix).
	binds []ast.Stmt
	at    []source.Span
	// labelBase is where this body's labels start: labels never cross a
	// literal.
	labelBase int
}

// litFrame is a fn literal being walked. Its captures are
// kept in source order with provisional indices until the frame pops,
// then sorted by name bytes.
type litFrame struct {
	rec  *itemRec
	base int // index of the literal's body in walker.bodies
	// loops is len(walker.fieldsLoops) when the literal began: the fields
	// loops whose bodies contain it.
	loops int
	caps  []Capture
	index map[source.Symbol]int
	// uses are the nodes whose Ref is a RefCapture into this frame, with a
	// provisional A; inner are the inner literals whose Capture.Of is.
	uses  []ast.NodeID
	inner []innerCap
}

// innerCap is an inner literal's capture of name, whose Of points into the
// frame that holds it.
type innerCap struct {
	info *ItemInfo
	name source.Symbol
}

// capture adds c to the frame if its name is new and returns the name's
// provisional index, and whether it was new.
func (l *litFrame) capture(c Capture) (uint32, bool) {
	if l.index == nil {
		l.index = map[source.Symbol]int{}
	}
	if i, ok := l.index[c.Name]; ok {
		return uint32(i), false
	}
	l.index[c.Name] = len(l.caps)
	l.caps = append(l.caps, c)
	return uint32(len(l.caps) - 1), true
}

func newWalker(m *Module, t *task, fs *fileState) *walker {
	return &walker{m: m, t: t, fs: fs, causes: map[causeKey]source.Span{}}
}

func (w *walker) innermostLiteral() *litFrame {
	if len(w.lits) == 0 {
		return nil
	}
	return w.lits[len(w.lits)-1]
}

// pushBody starts a body for rec; with attach, its locals become
// rec.info.Body (an interface method's parameters belong to no item).
func (w *walker) pushBody(rec *itemRec, attach bool) *bodyFrame {
	b := &Body{Item: rec.item.ID, Locals: make([]Local, 1)}
	if attach {
		rec.info.Body = b
	}
	f := &bodyFrame{rec: rec, body: b, binds: make([]ast.Stmt, 1), at: make([]source.Span, 1), labelBase: len(w.labels)}
	w.bodies = append(w.bodies, f)
	return f
}

// popBody ends the current body and reports its unused locals.
func (w *walker) popBody() {
	f := w.bodies[len(w.bodies)-1]
	w.unusedLocals(f)
	w.bodies = w.bodies[:len(w.bodies)-1]
	idx := len(w.bodies)
	n := len(w.locals)
	for n > 0 && w.locals[n-1].body >= idx {
		n--
	}
	w.locals = w.locals[:n]
}

// mark and release delimit a block scope.
func (w *walker) mark() int        { return len(w.locals) }
func (w *walker) release(mark int) { w.locals = w.locals[:mark] }

// declare adds a local to the current body. The comparison
// with every visible local is folded; an equal one is E0206 at the new
// declaration, which is still declared so its uses resolve to it. Every
// name it declares always binds, an `if x := e` binding included, so each
// takes the case rule here.
func (w *walker) declare(id *ast.Ident, kind LocalKind, isVar bool, stmt ast.Stmt) LocalID {
	return w.declareLocal(id, kind, isVar, stmt, false)
}

// declareLocal is declare; pattern marks a binding of a refutable pattern
// (declarePattern), whose case namePattern judges (patternCase). Its E0206
// over a const generic, or under a name that is not snake_case, offers no
// rename: renaming it would keep an arm that matches every value.
func (w *walker) declareLocal(id *ast.Ident, kind LocalKind, isVar bool, stmt ast.Stmt, pattern bool) LocalID {
	if id == nil || !declarable(id.Name) || len(w.bodies) == 0 {
		// No body frame: a binding written where no body is, such as an
		// else handler inside a gen line's arguments, whose parse or
		// check error is reported elsewhere.
		if id != nil {
			w.fs.setRef(id.ID(), poisonRef)
		}
		return 0
	}
	name := w.m.str(id.Name)
	fold := Fold(name)
	var shadow *diag.Diagnostic
	for i := len(w.locals) - 1; i >= 0; i-- {
		if w.locals[i].fold == fold {
			shadow = dup(id.Span(), w.locals[i].at, name, "local")
			shadow.Message = "`" + name + "` shadows the local declared before it"
			w.t.diags = append(w.t.diags, shadow)
			break
		}
	}
	constParam := false
	if shadow == nil {
		shadow = w.shadowsConstParam(id, name)
		constParam = shadow != nil
	}
	f := w.bodies[len(w.bodies)-1]
	lid := LocalID(len(f.body.Locals))
	f.body.Locals = append(f.body.Locals, Local{Name: id.Name, Decl: id.ID(), Kind: kind, Var: isVar})
	f.binds = append(f.binds, stmt)
	f.at = append(f.at, id.Span())
	w.locals = append(w.locals, scoped{sym: id.Name, fold: fold, local: lid, body: len(w.bodies) - 1, at: id.Span()})
	w.fs.setRef(id.ID(), w.localRef(lid))
	if shadow != nil {
		if pattern && (constParam || !IsSnake(name)) {
			shadow.Alt("compare with the value in an `if`, or rename the binding")
		} else {
			w.renameFix(shadow, id, name, lid)
		}
	}
	if !pattern && !IsSnake(name) {
		w.recaseLocal(id, name, lid, shadow != nil, "recase the local and its uses")
	}
	return lid
}

// shadowsConstParam is the E0206 of a local or parameter spelled exactly as
// a const generic in scope, which it would hide; nil when it is not one. The
// test is exact: a type parameter `T` and a local `t` stand side by side, as
// a const generic `N` and a local `n` do.
func (w *walker) shadowsConstParam(id *ast.Ident, name string) *diag.Diagnostic {
	for i := len(w.tparams) - 1; i >= 0; i-- {
		if g := w.tparams[i]; g.konst && g.sym == id.Name {
			d := dup(id.Span(), g.at, name, "local")
			d.Message = "`" + name + "` shadows the const parameter declared before it"
			w.t.diags = append(w.t.diags, d)
			return d
		}
	}
	return nil
}

// recaseLocal reports E0207 (b) for a local or parameter. The recase is a
// proven fix only when the recased name is free: a visible local with the
// same folded name (the E0206 just reported) or an undeclarable spelling
// (`True` to `true`) makes it an alternative.
func (w *walker) recaseLocal(id *ast.Ident, name string, lid LocalID, shadowed bool, why string) {
	want := ToSnake(name)
	d := naming(id.Span(), name, want, "snake_case")
	w.t.diags = append(w.t.diags, d)
	if shadowed || !w.free(want) {
		d.Alt("rename `" + name + "` to a free snake_case name")
		return
	}
	w.t.fixups = append(w.t.fixups, fixup{d: d, file: w.fs.idx, target: w.localRef(lid), text: want, decl: id.Span(), why: why})
}

// free reports whether a spelling may be declared as a local here: it is
// declarable and no module-level name has it.
func (w *walker) free(name string) bool {
	sym, ok := w.m.s.in.Lookup(name)
	if !ok {
		return true
	}
	if !declarable(sym) {
		return false
	}
	_, taken := w.m.rec.names.Get(sym)
	return !taken
}

// renameFix is E0206's proven fix for a local or parameter: append the next
// free digit at the declaration and every use. The base is the
// snake_case form, so the renamed local is no E0207 either.
func (w *walker) renameFix(d *diag.Diagnostic, id *ast.Ident, name string, lid LocalID) {
	if !IsSnake(name) {
		name = ToSnake(name)
	}
	text := nextFree(name, func(c string) bool {
		f := Fold(c)
		for _, l := range w.locals {
			if l.fold == f {
				return true
			}
		}
		_, ok := w.m.rec.names.Get(w.m.s.in.Intern(c))
		return ok
	})
	w.t.fixups = append(w.t.fixups, fixup{d: d, file: w.fs.idx, target: w.localRef(lid), text: text, decl: id.Span(), why: "rename the later declaration"})
}

// localRef is the Ref of a local of the current body.
func (w *walker) localRef(lid LocalID) Ref {
	f := w.bodies[len(w.bodies)-1]
	kind := RefLocal
	if int(lid) < len(f.body.Locals) {
		if k := f.body.Locals[lid].Kind; k == LocalParam || k == LocalSelf {
			kind = RefParam
		}
	}
	return Ref{Kind: kind, Segs: 1, A: uint32(lid), B: uint32(f.rec.item.ID)}
}

// markUsed marks the local a Ref names, in whichever body holds it.
func (w *walker) markUsed(r Ref) {
	for _, f := range w.bodies {
		if uint32(f.rec.item.ID) == r.B && int(r.A) < len(f.body.Locals) {
			f.body.Locals[r.A].Used = true
		}
	}
}

// declareParam declares a parameter; parameters are exempt from E0208.
// typ is its declared type, read only to note a const parameter of fn type
// (ConstFn).
func (w *walker) declareParam(id *ast.Ident, isVar, isConst bool, typ ast.Type) {
	if id == nil {
		return
	}
	if !declarable(id.Name) {
		w.fs.setRef(id.ID(), poisonRef)
		// keep the numbering: a parameter is local i whatever its name
		f := w.bodies[len(w.bodies)-1]
		f.body.Locals = append(f.body.Locals, Local{Name: id.Name, Decl: id.ID(), Kind: LocalParam, Var: isVar, Const: isConst, Used: true})
		f.binds = append(f.binds, nil)
		f.at = append(f.at, id.Span())
		return
	}
	name := w.m.str(id.Name)
	f := w.bodies[len(w.bodies)-1]
	fold := Fold(name)
	var shadow *diag.Diagnostic
	for i := len(w.locals) - 1; i >= 0; i-- {
		if w.locals[i].fold == fold {
			shadow = dup(id.Span(), w.locals[i].at, name, "parameter")
			w.t.diags = append(w.t.diags, shadow)
			break
		}
	}
	if shadow == nil {
		shadow = w.shadowsConstParam(id, name)
	}
	lid := LocalID(len(f.body.Locals))
	f.body.Locals = append(f.body.Locals, Local{Name: id.Name, Decl: id.ID(), Kind: LocalParam, Var: isVar, Const: isConst, Used: true})
	f.binds = append(f.binds, nil)
	f.at = append(f.at, id.Span())
	w.locals = append(w.locals, scoped{sym: id.Name, fold: fold, local: lid, body: len(w.bodies) - 1, at: id.Span(), constFn: isConst && isFnType(typ)})
	w.fs.setRef(id.ID(), w.localRef(lid))
	if shadow != nil {
		w.renameFix(shadow, id, name, lid)
	}
	if !IsSnake(name) {
		w.recaseLocal(id, name, lid, shadow != nil, "recase the parameter and its uses")
	}
}

// isFnType reports whether a declared type is a fn type after
// parentheses: the syntactic test of ConstFn (check re-tests with
// the real type, so an alias of a fn type is check's).
func isFnType(t ast.Type) bool {
	for {
		switch x := t.(type) {
		case *ast.ParenType:
			t = x.Type
		case *ast.FnType:
			return true
		default:
			return false
		}
	}
}

// declareSelf adds the receiver as local 1.
func (w *walker) declareSelf(at source.Span) {
	f := w.bodies[len(w.bodies)-1]
	lid := LocalID(len(f.body.Locals))
	f.body.Locals = append(f.body.Locals, Local{Name: source.SymSelf, Kind: LocalSelf, Used: true})
	f.binds = append(f.binds, nil)
	f.at = append(f.at, at)
	w.locals = append(w.locals, scoped{sym: source.SymSelf, fold: "self", local: lid, body: len(w.bodies) - 1, at: at})
}

// pushTypeParams brings a declaration's generics into scope. With check,
// the ones the declaration itself declares (not those inherited from an
// impl) are checked for the case rule and folded uniqueness (E0206, E0207
// (b)); a dependent item (invariant, contract) passes false.
func (w *walker) pushTypeParams(gens []genParam, check bool) int {
	mark := len(w.tparams)
	seen := map[string]source.Span{}
	for _, g := range gens {
		name := w.m.str(g.sym)
		f := Fold(name)
		if check && !g.inherited && !g.kindPending {
			if first, ok := seen[f]; ok {
				w.t.diags = append(w.t.diags, dup(g.at, first, name, "type parameter"))
			} else if !g.konst && !IsPascal(name) && declarable(g.sym) {
				w.t.diags = append(w.t.diags, naming(g.at, name, ToPascal(name), "PascalCase"))
			}
		}
		if _, ok := seen[f]; !ok {
			seen[f] = g.at
		}
		if declarable(g.sym) {
			w.tparams = append(w.tparams, g)
		}
	}
	return mark
}

// literal lifts a fn literal to an item and walks it.
func (w *walker) literal(lit *ast.FnLit) {
	parent := w.rec
	key := types.LiteralKey(parent.item.Key, parent.litN)
	parent.litN++
	at := keywordSpan(w.fs.fi.File, w.fs.fi.FileID, lit.Span().Start, ast.TokFn)
	var r *itemRec
	if id, ok := w.m.s.cfg.Items.ByKey(key); ok && w.m.byID[id] != nil {
		r = w.m.byID[id] // a re-resolved task keeps its literals (Settle)
		r.info.Literals = nil
		r.litN = 0
		r.info.Reads = types.ReadSet{}
	} else {
		it := &types.Item{Module: w.m.mod.ID, Owner: parent.item.ID, Kind: types.KindLiteral, Span: at, Key: key}
		w.m.s.cfg.Items.AddItem(it)
		r = &itemRec{info: &ItemInfo{Item: it.ID, File: w.fs.idx, Node: lit.ID(), Parent: parent.item.ID}, item: it, node: lit, file: w.fs.idx, parent: parent}
		w.t.literals = append(w.t.literals, r)
	}
	parent.info.Literals = append(parent.info.Literals, r.item.ID)
	w.fs.setRef(lit.ID(), itemRef(r.item.ID))
	w.rec = r
	w.pushBody(r, true)
	frame := &litFrame{rec: r, base: len(w.bodies) - 1, loops: len(w.fieldsLoops)}
	w.lits = append(w.lits, frame)
	if lit.Self != nil {
		w.declareSelf(lit.Self.Span())
	}
	for _, p := range lit.Params {
		if p == nil {
			continue
		}
		w.typ(p.Type)
	}
	for _, p := range lit.Params {
		if p != nil {
			w.declareParam(p.Name, p.Var, p.Const, p.Type)
		}
	}
	w.paramFlows(lit.Self, lit.Params)
	w.typ(lit.Result)
	w.block(lit.Body)
	w.lits = w.lits[:len(w.lits)-1]
	w.popBody()
	w.rec = parent
	w.closeCaptures(frame)
}

// closeCaptures sorts a popped literal's captures by name bytes, rewrites
// the provisional indices of its uses and of the inner literals' Of that
// point into it, and stores ItemInfo.Captures. Resolve reports no
// E0609 for a capture: whether the literal stands as a const argument is
// check's.
func (w *walker) closeCaptures(f *litFrame) {
	f.rec.info.Captures = nil
	if len(f.caps) == 0 {
		return
	}
	order := make([]int, len(f.caps))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return w.m.str(f.caps[order[a]].Name) < w.m.str(f.caps[order[b]].Name)
	})
	newIdx := make([]uint32, len(f.caps))
	sorted := make([]Capture, len(f.caps))
	for k, old := range order {
		newIdx[old] = uint32(k)
		sorted[k] = f.caps[old]
	}
	refs := w.fs.fi.Refs
	for _, n := range f.uses {
		if int(n) < len(refs) && refs[n].Kind == RefCapture && refs[n].B == uint32(f.rec.item.ID) && int(refs[n].A) < len(newIdx) {
			refs[n].A = newIdx[refs[n].A]
		}
	}
	for _, ic := range f.inner {
		for k := range ic.info.Captures {
			c := &ic.info.Captures[k]
			if c.Name == ic.name && c.Of.Kind == RefCapture && c.Of.B == uint32(f.rec.item.ID) && int(c.Of.A) < len(newIdx) {
				c.Of.A = newIdx[c.Of.A]
			}
		}
	}
	f.rec.info.Captures = sorted
}

// unusedLocals reports E0208 for the body's unused locals.
func (w *walker) unusedLocals(f *bodyFrame) {
	if w.fs.testPrelude {
		return
	}
	for i := 1; i < len(f.body.Locals); i++ {
		l := f.body.Locals[i]
		if l.Used || l.Decl == ast.NoNode {
			continue
		}
		switch l.Kind {
		case LocalBind, LocalDestr, LocalPattern, LocalLoop, LocalScope, LocalElse:
		default:
			continue
		}
		at := f.at[i]
		name := w.m.str(l.Name)
		d := diag.New(diag.E0208, at, "`"+name+"` is never used").Text("name", name)
		switch l.Kind {
		case LocalBind:
			if bs, ok := f.binds[i].(*ast.BindStmt); ok && bs.Value != nil && isPlacePath(bs.Value) {
				d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: lineSpan(w.fs.fi.File, bs.Span())}}, Why: "the statement has no effect"})
			}
		case LocalLoop, LocalPattern:
			d.Alt("write `_`")
		case LocalElse:
			// `else e { }` with e unused: delete ` e`, the handler then
			// binds nothing.
			d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: withSpaceBefore(w.fs.fi.File, at)}}, Why: "the handler does not use the error"})
		}
		w.t.diags = append(w.t.diags, d)
	}
}

// isPlacePath reports whether e is a literal or a place path (names and
// .field steps only): deleting its binding changes nothing (E0208 fix).
func isPlacePath(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.BasicLit, *ast.Ident:
		return true
	case *ast.FieldExpr:
		return isPlacePath(e.X)
	case *ast.UnaryExpr:
		return e.Op == ast.TokMinus && isLit(e.X)
	}
	return false
}

func isLit(e ast.Expr) bool { _, ok := e.(*ast.BasicLit); return ok }

// setOrigin finds the NameOrigin of an ident of a generated file.
func (w *walker) setOrigin(id *ast.Ident) {
	if w.fs.origins != nil {
		w.origin = w.fs.origins[id.Span().Start]
	}
}

func (w *walker) clearOrigin() { w.origin = NameOrigin{} }

// pinned resolves a pinned token of generated code as given.
func (w *walker) pinned(id *ast.Ident) (Ref, bool) {
	switch w.origin.Kind {
	case NamePinnedItem:
		return itemRef(w.origin.Item), true
	case NamePinnedModule:
		return Ref{Kind: RefModule, Segs: 1, A: uint32(w.origin.Module)}, true
	}
	return Ref{}, false
}

// sortedTaskDiags orders a task's diagnostics by position (merge order is
// by task key; within a task, by source).
func sortedTaskDiags(ds []*diag.Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].At.Start < ds[j].At.Start })
}
