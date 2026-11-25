// Why: inside a body, every statement and pattern introduces or uses names,
// and each needs its own scoping rule before check can type it.
//
// What: the walker's statement and pattern methods: block, stmt, ifStmt,
// cond, forStmt, labelUse, recordPat, pattern, patFields, declarePattern
// namePattern and patternCase.
//
// How: one switch per statement kind. A declaration resolves its initializer
// before it declares its names, so x := x + 1 reads an outer x. A bare name
// in a pattern is an error test, a value test (a const, true, false), E0101
// or a fresh binding, decided by what it names; in an alternative it never
// binds. A binding outside a destructure must be snake_case, so a name in a
// const's place that is no const never quietly matches every value.

package resolve

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// block walks a block in a scope of its own.
func (w *walker) block(b *ast.Block) {
	if b == nil {
		return
	}
	mark := w.mark()
	for _, s := range b.Stmts {
		w.stmt(s)
	}
	w.release(mark)
}

// stmt walks one statement. A declaration resolves its initializer before it
// declares its names, so x := x + 1 reads an outer x.
func (w *walker) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.BindStmt:
		w.typ(s.Type)
		w.expr(s.Value)
		w.declare(s.Name, LocalBind, s.Var, s)
	case *ast.DestructureStmt:
		w.expr(s.Value)
		w.recordPat(s.Pat, s.Var, s)
	case *ast.DiscardStmt:
		w.expr(s.Value)
	case *ast.AssignStmt:
		w.expr(s.Target)
		w.expr(s.Value)
	case *ast.IfStmt:
		w.ifStmt(s)
	case *ast.ForStmt:
		w.forStmt(s)
	case *ast.MatchStmt:
		w.expr(s.X)
		for _, arm := range s.Arms {
			if arm == nil {
				continue
			}
			mark := w.mark()
			w.pattern(arm.Pat, false)
			w.block(arm.Body)
			w.release(mark)
		}
	case *ast.ReturnStmt:
		w.expr(s.Value)
	case *ast.FailStmt:
		w.expr(s.Value)
	case *ast.BecomeStmt:
		w.expr(s.Call) // its callee and arguments resolve as a call's
	case *ast.BreakStmt:
		w.labelUse(s.Label)
	case *ast.ContinueStmt:
		w.labelUse(s.Label)
	case *ast.DeferStmt:
		if s.Body != nil {
			w.block(s.Body)
		} else {
			mark := w.mark()
			w.expr(s.X)
			w.release(mark)
		}
	case *ast.ScopeStmt:
		w.expr(s.Value)
		mark := w.mark()
		w.declare(s.Name, LocalScope, false, s)
		w.block(s.Body)
		w.release(mark)
	case *ast.UnsafeStmt:
		w.block(s.Body)
	case *ast.AsmStmt:
		// A sym operand's path resolves as a value path; an in value, an out
		// place and a const operand as expressions.
		for _, op := range s.Operands {
			switch {
			case op.Sym != nil:
				w.path(op.Sym, posValue)
			case op.Value != nil:
				w.expr(op.Value)
			}
		}
	case *ast.ExprStmt:
		w.expr(s.X)
	case *ast.BadStmt:
	}
}

// ifStmt walks an if chain.
func (w *walker) ifStmt(s *ast.IfStmt) {
	mark := w.mark()
	w.cond(s.Cond, LocalPattern)
	w.block(s.Then)
	w.release(mark)
	switch e := s.Else.(type) {
	case *ast.IfStmt:
		w.ifStmt(e)
	case *ast.Block:
		w.block(e)
	}
}

// cond walks an if or for condition; a binding is declared in the caller's
// scope around the block.
func (w *walker) cond(c ast.Node, kind LocalKind) {
	switch c := c.(type) {
	case nil:
	case *ast.BindCond:
		w.expr(c.Value)
		w.declare(c.Name, kind, false, nil)
	case *ast.IsCond:
		w.isSubject(c.Name)
		w.typ(c.Type)
	case ast.Expr:
		w.expr(c)
	}
}

// isSubject binds the T of `if T is X` to a type parameter.
func (w *walker) isSubject(id *ast.Ident) {
	if id == nil {
		return
	}
	for i := len(w.tparams) - 1; i >= 0; i-- {
		if g := w.tparams[i]; g.sym == id.Name {
			w.fs.setRef(id.ID(), Ref{Kind: RefTypeParam, Segs: 1, A: uint32(g.idx), B: uint32(g.item)})
			return
		}
	}
	spell := w.m.str(id.Name)
	d := diag.New(diag.E0201, id.Span(), "`"+spell+"` is not a type parameter").Text("name", spell).Text("expected", "type parameter")
	w.report(d, id.Name)
	w.fs.setRef(id.ID(), poisonRef)
}

// forStmt walks a loop: its label, its head, then its body.
func (w *walker) forStmt(s *ast.ForStmt) {
	labels := len(w.labels)
	if s.Label != nil {
		name := w.m.str(s.Label.Name)
		if !IsSnake(name) {
			w.t.diags = append(w.t.diags, naming(s.Label.Span(), name, ToSnake(name), "snake_case"))
		}
		base := w.bodies[len(w.bodies)-1].labelBase
		for i := len(w.labels) - 1; i >= base; i-- {
			if Fold(w.m.str(w.labels[i].sym)) == Fold(name) {
				w.t.diags = append(w.t.diags, dup(s.Label.Span(), w.labels[i].at, name, "label"))
				break
			}
		}
		w.labels = append(w.labels, label{sym: s.Label.Name, node: s.ID(), body: len(w.bodies) - 1, at: s.Label.Span()})
		w.fs.setRef(s.Label.ID(), Ref{Kind: RefLabel, Segs: 1, A: uint32(s.ID())})
	}
	mark := w.mark()
	fieldsLoop := false
	switch h := s.Head.(type) {
	case *ast.InHead:
		switch src := h.Source.(type) {
		case *ast.RangeSource:
			w.expr(src.Lo)
			w.expr(src.Hi)
		case *ast.RefSource:
			w.expr(src.X)
		case *ast.FieldsSource:
			w.expr(src.X)
			// A literal in this loop's body cannot capture what the
			// loop head or body declares (FieldsLoop).
			w.fieldsLoops = append(w.fieldsLoops, fieldsFrame{locals: len(w.locals), body: len(w.bodies) - 1})
			fieldsLoop = true
		}
		if h.Key != nil {
			w.declare(h.Key, LocalLoop, false, nil)
		}
		if h.Value != nil {
			w.declare(h.Value, LocalLoop, false, nil)
		}
		if h.Pat != nil {
			// Each name a destructured loop variable binds is a loop
			// local of the body's scope, as Key and Value are.
			for _, f := range h.Pat.Fields {
				if f == nil {
					continue
				}
				if f.Pat == nil {
					w.declare(f.Name, LocalLoop, false, nil)
				} else {
					w.pattern(f.Pat, false)
				}
			}
		}
	default:
		w.cond(s.Head, LocalLoop)
	}
	w.block(s.Body)
	if fieldsLoop {
		w.fieldsLoops = w.fieldsLoops[:len(w.fieldsLoops)-1]
	}
	w.release(mark)
	w.labels = w.labels[:labels]
}

// labelUse binds a break or continue label; labels do not cross
// a fn literal.
func (w *walker) labelUse(id *ast.Ident) {
	if id == nil {
		return
	}
	base := w.bodies[len(w.bodies)-1].labelBase
	for i := len(w.labels) - 1; i >= base; i-- {
		if w.labels[i].sym == id.Name {
			w.fs.setRef(id.ID(), Ref{Kind: RefLabel, Segs: 1, A: uint32(w.labels[i].node)})
			return
		}
	}
	spell := w.m.str(id.Name)
	d := diag.New(diag.E0201, id.Span(), "no enclosing loop is labeled `"+spell+"`").Text("name", spell).Text("expected", "label")
	w.report(d, id.Name)
	w.fs.setRef(id.ID(), poisonRef)
}

// recordPat walks the left side of a destructuring bind: its path in type
// position, then its fields (a field with no pattern declares a local).
func (w *walker) recordPat(p *ast.RecordPat, isVar bool, s ast.Stmt) {
	if p == nil {
		return
	}
	if p.Path != nil {
		w.path(p.Path, posType, p.ID())
	}
	for _, f := range p.Fields {
		if f == nil {
			continue
		}
		if f.Pat == nil {
			w.declare(f.Name, LocalDestr, isVar, s)
		} else {
			// A labeled field's sub-pattern binds, and `var` sets Var on
			// every binding of the statement, nested ones included.
			outer := w.destr
			w.destr = &destrCtx{isVar: isVar, stmt: s}
			w.pattern(f.Pat, false)
			w.destr = outer
		}
	}
}

// destrCtx is the destructuring statement whose sub-patterns are being
// walked: their bindings are LocalDestr and carry the statement's `var`.
type destrCtx struct {
	isVar bool
	stmt  ast.Stmt
}

// declarePattern declares a binding of a pattern: LocalPattern in a match,
// if or for, LocalDestr with the statement's `var` inside a destructure. A
// destructure cannot fail, so its bindings take a local's case rule; a
// refutable pattern's name is namePattern's to judge (patternCase), and a
// field written alone is check's (E0309).
func (w *walker) declarePattern(id *ast.Ident) {
	if w.destr != nil {
		w.declareLocal(id, LocalDestr, w.destr.isVar, w.destr.stmt, false)
		return
	}
	w.declareLocal(id, LocalPattern, false, nil, true)
}

// pattern walks a match pattern; inAlt marks an alternative of an
// AltPattern, where a bare name is never a binding.
func (w *walker) pattern(p ast.Pattern, inAlt bool) {
	switch p := p.(type) {
	case *ast.NamePattern:
		w.namePattern(p, inAlt)
	case *ast.PathPattern:
		w.path(p.Path, posPattern, p.ID())
		w.patFields(p.Fields)
	case *ast.VariantPattern:
		w.patFields(p.Fields)
	case *ast.AltPattern:
		for _, a := range p.Alts {
			w.pattern(a, true)
		}
	case *ast.SomePattern:
		w.pattern(p.Pat, inAlt)
	case *ast.OkPattern:
		w.pattern(p.Pat, inAlt)
	case *ast.ErrPattern:
		w.pattern(p.Pat, inAlt)
	}
}

// patFields walks the fields of a variant, error or record pattern.
func (w *walker) patFields(fs []*ast.PatField) {
	for _, f := range fs {
		if f == nil {
			continue
		}
		if f.Pat == nil {
			w.declarePattern(f.Name)
		} else {
			w.pattern(f.Pat, false)
		}
	}
}

// namePattern decides a bare name in a pattern: an error
// type is an error test, a const, true and false are value tests, a bare
// ok or err is E0101, anything else is a fresh binding.
func (w *walker) namePattern(p *ast.NamePattern, inAlt bool) {
	id := p.Name
	if id == nil {
		return
	}
	if id.Name == source.SymOk || id.Name == source.SymErr {
		spell := w.m.str(id.Name)
		d := diag.New(diag.E0101, id.Span(), "write `"+spell+"(_)`").In(diag.PhaseNames).Text("expected", "pattern")
		w.t.diags = append(w.t.diags, d)
		w.fs.setRef(id.ID(), poisonRef)
		w.fs.setRef(p.ID(), poisonRef)
		return
	}
	h, ok := w.find(id.Name)
	if ok && h.step != 1 {
		w.recordReads(id.Name, h, ok)
	} else if !ok {
		w.recordReads(id.Name, h, ok)
	}
	if ok {
		switch h.ref.Kind {
		case RefItem:
			it := w.m.s.cfg.Items.Get(types.ItemID(h.ref.A))
			switch it.Kind {
			case types.KindError, types.KindConst:
				w.fs.setRef(id.ID(), h.ref)
				w.fs.setRef(p.ID(), h.ref)
				return
			}
		case RefUniverse:
			if s := source.Symbol(h.ref.A); s == source.SymTrue || s == source.SymFalse {
				w.fs.setRef(id.ID(), h.ref)
				w.fs.setRef(p.ID(), h.ref)
				return
			}
		}
		if inAlt {
			r := h.ref
			if h.step == 1 {
				r = w.useLocal(id, h)
			}
			w.fs.setRef(id.ID(), r)
			w.fs.setRef(p.ID(), r)
			return
		}
	}
	if inAlt {
		r := w.miss(id, posPattern, p.ID())
		w.fs.setRef(id.ID(), r)
		w.fs.setRef(p.ID(), r)
		w.patchPending(r, []holder{{node: p.ID(), covers: 1}}, nil)
		return
	}
	shadows := len(w.t.diags)
	w.declarePattern(id)
	w.fs.setRef(p.ID(), w.fs.fi.Refs[id.ID()])
	if w.destr == nil && len(w.t.diags) == shadows {
		w.patternCase(id, ok)
	}
}

// patternCase is the E0207 of a refutable pattern's binding that is not
// snake_case: a name in a const's place that names no const or error in
// scope (a misspelled one, another module's written bare, a variant without
// its dot)
// would bind and match every value. No recase is proven, since a recased
// binding still matches everything. In a module whose gen lines are still
// unsettled, a name found nowhere may yet be generated: Settle reports it
// then (casePend), or re-resolves the item when the name has appeared.
func (w *walker) patternCase(id *ast.Ident, found bool) {
	name := w.m.str(id.Name)
	if IsSnake(name) {
		return
	}
	// One diagnostic for the one mistake: no E0208 besides.
	w.markUsed(w.fs.fi.Refs[id.ID()])
	want := ToSnake(name)
	d := naming(id.Span(), name, want, "snake_case")
	d.Message = "`" + name + "` names no const or error in scope, so it binds and matches every value: a binding should be snake_case: `" + want + "`"
	d.Alt("write `." + name + "` for a variant")
	d.Alt("name a const or error in scope, or qualify another module's as `m." + name + "`")
	if w.free(want) {
		d.Alt("rename the binding `" + want + "`")
	}
	if !found && w.m.missesPend() {
		w.t.casePend = append(w.t.casePend, casePend{sym: id.Name, d: d})
		return
	}
	w.t.diags = append(w.t.diags, d)
}
