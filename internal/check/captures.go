// Why: a fn literal that captures locals is safe only where its environment
// of borrowed locals cannot outlive them: as the whole argument of a const
// parameter. The rules for where it stands and what its body may do with a
// capture live here and in captures_mut.go.
//
// What: LitEnv and CaptureType (the environment), literalCaptures,
// captureSource and waitCaptures (typing it on both sides), captureMove
// (E0609), judgeConstFnUses (E0705 for a misused const fn parameter),
// CallbackRecipe.
//
// How: when the enclosing unit types a capturing literal in a const fn
// argument position, it types the literal eagerly with the captured
// variables' origins (type, var-ness, contract/invariant flag) so
// judgeCaptureUses decides per capture whether the environment holds &T,
// mut T or a reborrowed mut T / mut []T. The checked literal body and
// environment type are published through captureRec; the literal's unit
// waits on done and returns that body directly.

package check

import (
	"sync"
	"time"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// LitEnv is a capturing literal's environment as the enclosing unit typed
// it.
type LitEnv struct {
	Lit  ast.NodeID   // the FnLit node in the enclosing unit
	Item types.ItemID // the literal item
	// Ref is &R (when every capture is read-only) or mut R (when at least one
	// capture is mutated or reborrowed): the hidden parameter's type.
	Ref types.TypeID
	// Sources are the captured names as the enclosing unit sees them, in
	// field order: a UseLocal of a local, a parameter or, in a literal that
	// captures them itself, its own capture local.
	Sources []Use
	// Modes records how each capture is borrowed into the environment record,
	// in field order.
	Modes []CaptureMode
}

// CaptureType is one field of a literal's environment: the captured name,
// its type T, and how it is captured (CapShared, CapMut or CapReborrow).
type CaptureType struct {
	Name source.Symbol
	Type types.TypeID
	Mode CaptureMode
}

// captureRec is one capturing literal's handoff from its enclosing unit to
// its own unit. The enclosing unit types the literal eagerly while typing
// the FnLit and closes done when its run ends, normally or by panic; the
// literal's unit waits on done, so the writes are ordered before its reads
// (Go memory model) and no lock is needed.
type captureRec struct {
	done   chan struct{}
	once   sync.Once
	set    bool // written once; later typings (a fields-loop copy) compare
	ok     bool
	types  []types.TypeID
	modes  []CaptureMode
	envRef types.TypeID
	body   *Body
	ice    *source.InternalError
	// diag is the enclosing unit's diagnostic that leaves the literal with
	// no environment; the literal's unit carries it too, so it is never
	// lowered (diag.Order keeps one of the two equal diagnostics).
	diag  *diag.Diagnostic
	reads types.ReadSet
}

func (r *captureRec) close() { r.once.Do(func() { close(r.done) }) }

func (r *captureRec) closed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// fail records that the literal gets no environment because of d (nil: an
// earlier error of the enclosing unit, filled in when it closes).
func (r *captureRec) fail(d *diag.Diagnostic) {
	if r.closed() || r.set {
		return
	}
	r.set, r.ok, r.diag = true, false, d
}

// captureRecOf is literal lit's record, made on first use by either side.
func (m *Module) captureRecOf(lit types.ItemID) *captureRec {
	m.capMu.Lock()
	defer m.capMu.Unlock()
	if m.caps == nil {
		m.caps = map[types.ItemID]*captureRec{}
	}
	r := m.caps[lit]
	if r == nil {
		r = &captureRec{done: make(chan struct{})}
		m.caps[lit] = r
	}
	return r
}

// captureWait bounds the literal unit's wait for its enclosing unit. The
// driver starts units in Units() order with a worker slot taken first, so
// the enclosing unit always runs; the bound turns a broken ordering
// into an ICE instead of a hang.
const captureWait = 2 * time.Minute

// closeLiteralRecords closes the records of the capturing literals this
// unit's item owns (run's defer): a record nothing wrote gets ok false and
// the unit's first diagnostic, so the literal unit accepts silently.
// An eager literal unit leaves closing to its top-level enclosing unit so
// that the enclosing unit's full read set is merged into every nested
// literal's body before any waiting Check call reads it.
func (u *unit) closeLiteralRecords() {
	if u.eager {
		return
	}
	u.closeDescendants(u.it.ID, &u.b.Reads, u.b.Diags)
}

func (u *unit) closeDescendants(owner types.ItemID, reads *types.ReadSet, diags []*diag.Diagnostic) {
	ii := u.m.byItem[owner]
	if ii == nil {
		return
	}
	for _, lit := range ii.Literals {
		li := u.m.byItem[lit]
		if li == nil || len(li.Captures) == 0 {
			// A non-capturing literal runs its own non-eager unit in Check and
			// closes its own nested capturing literals when it finishes.
			continue
		}
		r := u.m.captureRecOf(lit)
		if !r.closed() {
			if !r.set {
				r.set, r.ok = true, false
			}
			if !r.ok && r.diag == nil && len(diags) > 0 {
				r.diag = diags[0]
			}
			if r.ok {
				// The enclosing unit's read set when it ended: the
				// literal's covers every fact its environment's types came
				// from.
				if r.body != nil {
					r.body.Reads.Merge(reads)
				} else {
					var rs types.ReadSet
					rs.Merge(reads)
					r.reads = rs
				}
			}
		}
		// Only recurse into literals that were checked eagerly (r.body != nil
		// or r.ice != nil): a literal with r.body == nil && r.ice == nil runs
		// its own fallback non-eager unit in Check and closes its own nested
		// capturing literals when it finishes.
		if r.body != nil {
			u.closeDescendants(lit, &r.body.Reads, r.body.Diags)
		} else if r.ice != nil {
			u.closeDescendants(lit, reads, diags)
		}
		r.close()
	}
}

// markConstLit records in set that the FnLit or name a is, after
// parentheses, the whole argument of a const parameter of a call that is not
// s.go's (where a literal may stand, and how a const fn parameter is
// forwarded): set is constLit for a parameter of fn type, constNotFn for
// any other.
func (u *unit) markConstLit(set *Bits, a ast.Expr) {
	for {
		p, ok := a.(*ast.ParenExpr)
		if !ok {
			break
		}
		a = p.X
	}
	switch a.(type) {
	case *ast.FnLit, *ast.Ident: // a literal may capture; a const fn parameter is forwarded
	default:
		return
	}
	if set.w == nil {
		*set = newBits(len(u.b.Types))
	}
	u.b.slot(a.ID())
	set.set(a.ID(), u.b.Base)
}

// literalCaptures types FnLit e of literal item it, which
// captures ii.Captures: E0609 where it is no const argument, else the
// environment typed and published.
func (u *unit) literalCaptures(e *ast.FnLit, it *types.Item, ii *resolve.ItemInfo) {
	rec := u.m.captureRecOf(it.ID)
	caps := ii.Captures
	if !u.constLit.Has(e.ID(), u.b.Base) {
		first := caps[0].First
		var names []string
		for _, c := range caps {
			names = append(names, u.c.cfg.Interner.Str(c.Name))
			if c.First.Start < first.Start {
				first = c.First
			}
		}
		code, text := u.litContext(e)
		d := u.err(diag.E0609, first, "a `fn` literal that reads `"+u.c.cfg.Interner.Str(caps[0].Name)+
			"` is no value: only a literal written directly as the argument of a `const` parameter may read the enclosing function's locals (here it is "+text+")").
			Fact("captured", diag.TextList(names...)).Text("reason", code).Loc("literal", e.Span()).In(diag.PhaseTypes)
		captureAlts(d, false)
		rec.fail(d)
		return
	}
	for _, c := range caps {
		name := u.c.cfg.Interner.Str(c.Name)
		switch {
		case c.FieldsLoop:
			d := u.err(diag.E0609, c.First, "the literal reads `"+name+"`, which this `fields` loop declares; the loop body is checked once per field, so its literals cannot capture what it declares").
				Fact("captured", diag.TextList(name)).Text("reason", "fields-loop").In(diag.PhaseTypes)
			captureAlts(d, false)
			rec.fail(d)
			return
		case c.ConstFn || u.constFnSource(c.Of):
			// Resolve's flag is syntactic; an alias of a fn type is only
			// seen in the real type.
			d := u.err(diag.E0705, c.First, "a `fn` literal cannot read the `const` parameter `"+name+"`: call it outside the literal, or give the literal what it needs as a parameter").
				Text("param", name).Text("use", "captured")
			rec.fail(d)
			return
		}
	}
	ts := make([]types.TypeID, len(caps))
	origins := make([]captureOrigin, len(caps))
	srcs := make([]Use, len(caps))
	for i, c := range caps {
		orig, use := u.captureSource(c.Of, e)
		if orig.typ == types.Poison || orig.typ == types.NoType {
			// The source's type is unknown: this unit reported (or another
			// stage did) the error that hid it.
			rec.fail(nil)
			return
		}
		orig.typ = u.nt(orig.typ) // a literal in `if T is X` sees X
		ts[i], origins[i], srcs[i] = orig.typ, orig, use
	}
	var ref types.TypeID
	var modes []CaptureMode
	if !rec.closed() {
		if !rec.set {
			// Type the literal body eagerly so judgeCaptureUses decides which
			// captures need mut or reborrow and builds EnvRef before the
			// enclosing unit is lowered and borrow-checked.
			lu := u.m.newUnitWithCaptures(it, ii, origins)
			lu.x.rec.signature(it.Key)
			ie := source.Guard(it.Key.String(), it.Span, lu.run)
			if ie != nil {
				modes = make([]CaptureMode, len(caps))
				ref = envTypeOf(u.tab, caps, origins, modes)
				rec.set, rec.ok, rec.types, rec.modes, rec.envRef, rec.ice = true, true, ts, modes, ref, ie
			} else {
				modes = make([]CaptureMode, len(lu.b.Captures))
				for i, ct := range lu.b.Captures {
					modes[i] = ct.Mode
				}
				ref = lu.b.EnvRef
				u.b.Reads.Merge(&lu.b.Reads)
				rec.set, rec.ok, rec.types, rec.modes, rec.envRef, rec.body = true, true, ts, modes, ref, lu.b
			}
		} else if rec.ok {
			if !sameTypes(rec.types, ts) {
				source.ICEf(e.Span(), "check: a capturing literal typed twice with different capture types")
			}
			modes, ref = rec.modes, rec.envRef
		} else {
			return
		}
	} else if rec.ok {
		modes, ref = rec.modes, rec.envRef
	} else {
		return
	}
	for _, env := range u.b.Envs {
		if env.Lit == e.ID() {
			return // a fields-loop copy types the literal again
		}
	}
	u.b.Envs = append(u.b.Envs, LitEnv{Lit: e.ID(), Item: it.ID, Ref: ref, Sources: srcs, Modes: modes})
}

func sameTypes(a, b []types.TypeID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// captureSource is a capture's origin (type, var-ness, reborrowability,
// contract flag, declaration span) and its Use in this, the enclosing unit:
// a local's declared or inferred type, a parameter's, this literal's own
// capture, or, in a requires or ensures predicate, the owner function's
// parameter or result, which the predicate holds as its &P parameter: T_i is
// P, and the source is that &P local, which lower borrows through. A Ref this
// unit cannot map is an ICE at literal e: silent Poison would leave a valid
// program's literal with no environment.
func (u *unit) captureSource(of resolve.Ref, e *ast.FnLit) (captureOrigin, Use) {
	isReborrowType := func(t types.TypeID) bool {
		k := u.tab.Kind(t)
		return k == types.KMut || k == types.KMutSlice
	}
	switch of.Kind {
	case resolve.RefLocal:
		id := u.local(LocalID(of.A))
		if types.ItemID(of.B) == u.it.ID && int(id) < len(u.b.Locals) {
			loc := u.b.Locals[id]
			canAddVar := false
			if u.rb != nil && int(of.A) < len(u.rb.Locals) && u.rb.Locals[of.A].Kind == resolve.LocalBind {
				canAddVar = true
			}
			return captureOrigin{
				typ:       loc.Type,
				isVar:     loc.Var,
				reborrow:  isReborrowType(loc.Type),
				declSpan:  loc.Span,
				canAddVar: canAddVar,
			}, Use{Kind: UseLocal, Local: id}
		}
	case resolve.RefParam, resolve.RefResult:
		id := LocalID(of.A)
		if of.Kind == resolve.RefParam && types.ItemID(of.B) == u.it.ID && id >= 1 && int(id) < len(u.b.Locals) {
			loc := u.b.Locals[id]
			if _, isInv := u.b.Node.(*ast.InvariantMember); isInv {
				return captureOrigin{typ: loc.Type, contract: true, declSpan: loc.Span}, Use{Kind: UseLocal, Local: id}
			}
			isRecv := u.sig != nil && u.sig.Receiver != types.NoReceiver && id == 1
			isValSelf := isRecv && u.sig.Receiver == types.ByValue
			isConstParam := u.sig != nil && int(id) <= len(u.sig.Params) && u.sig.Params[id-1].Const
			_, isTest := u.b.Node.(*ast.TestItem)
			canAddVar := !isRecv && !isConstParam && !isTest && loc.Span.File != source.NoFile
			return captureOrigin{
				typ:       loc.Type,
				isVar:     loc.Var,
				reborrow:  isReborrowType(loc.Type),
				declSpan:  loc.Span,
				canAddVar: canAddVar,
				isValSelf: isValSelf,
			}, Use{Kind: UseLocal, Local: id}
		}
		if t, id, ok := u.predicateLocal(of); ok {
			if id == 0 {
				return captureOrigin{typ: types.Poison}, Use{}
			}
			return captureOrigin{typ: t, contract: true}, Use{Kind: UseLocal, Local: id}
		}
	case resolve.RefCapture:
		if types.ItemID(of.B) == u.it.ID && int(of.A) < len(u.b.Captures) {
			id := u.capBase + LocalID(of.A)
			return u.capOrigins[of.A], Use{Kind: UseLocal, Local: id}
		}
	}
	source.ICEf(e.Span(), "check: a capture of %s %d of item %d in the unit of %s", of.Kind, of.A, of.B, u.it.Key)
	return captureOrigin{typ: types.Poison}, Use{}
}

// constFnSource reports whether capture source of is a const parameter of
// fn type of the function that declares it, by its real type:
// this unit's own, or, in a contract predicate, its owner's.
func (u *unit) constFnSource(of resolve.Ref) bool {
	if of.Kind != resolve.RefParam || of.A == 0 {
		return false
	}
	var sig *types.Signature
	switch types.ItemID(of.B) {
	case u.it.ID:
		sig = u.b.Sig
	case u.it.Owner:
		if _, ok := u.b.Node.(*ast.Contract); ok {
			if fd, ok := u.c.cfg.Items.Get(u.it.Owner).Decl.(*types.FuncDecl); ok {
				sig = &fd.Sig
			}
		}
	}
	if sig == nil || int(of.A) > len(sig.Params) {
		return false
	}
	p := sig.Params[of.A-1]
	return p.Const && u.tab.Kind(p.Type) == types.KFn
}

// initCaptures installs one capture local per capture of literal item ii from
// origins (the eager check path, called from literalCaptures).
func (u *unit) initCaptures(ii *resolve.ItemInfo, origins []captureOrigin) {
	b := u.b
	if len(b.Locals) == 0 {
		b.Locals = make([]Local, 1)
	}
	u.capBase = LocalID(len(b.Locals))
	u.capOrigins = origins
	u.hasCaptures = true
	for i, c := range ii.Captures {
		t := origins[i].typ
		b.Locals = append(b.Locals, Local{Name: c.Name, Type: t, Var: origins[i].isVar, Capture: uint32(i) + 1, Span: c.First})
		b.Captures = append(b.Captures, CaptureType{Name: c.Name, Type: t})
	}
}

// waitCaptureRec waits for a capturing literal's enclosing unit to publish its
// captureRec.
func (m *Module) waitCaptureRec(it *types.Item) *captureRec {
	rec := m.captureRecOf(it.ID)
	select {
	case <-rec.done:
	case <-time.After(captureWait):
		source.ICEf(it.Span, "check: capturing literal %s waited %v for its enclosing unit (Units() order)", it.Key, captureWait)
	}
	return rec
}

// waitCaptures is the literal unit's fallback path when its enclosing unit
// did not check it eagerly (for instance, the literal stood outside a const
// argument, or a parse error hid it): add Poison locals and carry any
// enclosing diagnostic.
func (u *unit) waitCaptures(ii *resolve.ItemInfo, rec *captureRec) {
	b := u.b
	if len(b.Locals) == 0 {
		b.Locals = make([]Local, 1)
	}
	u.capBase = LocalID(len(b.Locals))
	for i, c := range ii.Captures {
		b.Locals = append(b.Locals, Local{Name: c.Name, Type: types.Poison, Capture: uint32(i) + 1, Span: c.First})
		b.Captures = append(b.Captures, CaptureType{Name: c.Name, Type: types.Poison})
	}
	if rec != nil && rec.diag != nil {
		b.Diags = append(b.Diags, rec.diag)
	}
}

// CallbackRecipe is the sentence every E0609 message ends with: the one
// way to keep a callback.
const CallbackRecipe = "; to store a callback, store a `fn` value and its plain state; see the cookbook's stored-callback recipe"

// captureAlts are the rewrites every E0609 form names.
func captureAlts(d *diag.Diagnostic, mutating bool) {
	d.Message += CallbackRecipe
	d.Alt("a struct holding what the callback needs, and a named `fn` that takes it").Alt("a loop instead of the callback")
	if mutating {
		d.Alt("pass the state `mut` through an explicit parameter")
	}
}

// litContext is the reason a capturing literal is no const argument:
// the context of FnLit e's nearest non-parenthesis ancestor in the unit.
func (u *unit) litContext(e *ast.FnLit) (code, text string) {
	var stack []ast.Node
	var parent, grand ast.Node
	ast.Inspect(u.b.Node, func(n ast.Node) bool {
		if parent != nil || grand != nil {
			return false
		}
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if n == ast.Node(e) {
			for i := len(stack) - 1; i >= 0; i-- {
				if _, ok := stack[i].(*ast.ParenExpr); ok {
					continue
				}
				parent = stack[i]
				if i > 0 {
					grand = stack[i-1]
				}
				break
			}
			if parent == nil {
				parent = u.b.Node
			}
			return false
		}
		stack = append(stack, n)
		return true
	})
	switch parent.(type) {
	case *ast.BindStmt, *ast.AssignStmt, *ast.DestructureStmt:
		return "bound", "bound to a name"
	case *ast.ReturnStmt:
		return "returned", "returned"
	case *ast.Arg:
		if c, ok := grand.(*ast.CallExpr); ok {
			if u.goCalls != nil && u.goCalls[c.ID()] {
				return "spawn", "in an `s.go` call"
			}
			if u.constructs(c) {
				return "stored", "stored in a value"
			}
		}
		if u.constNotFn.Has(e.ID(), u.b.Base) {
			// The callee may store or return a const F.
			return "argument", "passed to a `const` parameter whose type is not a `fn` type, which the callee may use as any value"
		}
		return "argument", "passed to a parameter that is not `const`"
	case *ast.RecField, *ast.RecordLit, *ast.ArrayLit:
		return "stored", "stored in a value"
	case *ast.IndexExpr:
		return "bracket", "a bracket argument"
	}
	return "value", "used as a value"
}

// constructs reports whether call c builds a value (a struct, `some`,
// `ok`): its arguments are stored, not passed. Read from resolution, since
// the literal is typed before the call's Use is recorded.
func (u *unit) constructs(c *ast.CallExpr) bool {
	id, ok := c.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	switch u.c.cfg.Interner.Str(id.Name) {
	case "some", "ok", "err":
		return true
	}
	if r := u.ref(id); r.Kind == resolve.RefItem {
		k := u.c.cfg.Items.Get(types.ItemID(r.A)).Kind
		return k == types.KindStruct || k == types.KindError
	}
	return false
}

// useOf is node id's Use, the zero Use when none was recorded.
func (u *unit) useOf(id ast.NodeID) Use {
	if u.b.Uses == nil {
		return Use{}
	}
	i := int(id) - int(u.b.Base)
	if i < 0 || i >= len(u.b.Uses) {
		return Use{}
	}
	return u.b.Uses[i]
}

// captureMove is E0609 move: a non-Plain capture in a value position.
func (u *unit) captureMove(root *ast.Ident, ci int, t types.TypeID) {
	n := u.c.cfg.Interner.Str(u.b.Captures[ci].Name)
	d := u.err(diag.E0609, root.Span(), "the literal moves `"+n+"`, which it captured; a capture is borrowed and cannot be moved out of, and only a plain one is copied").
		Fact("captured", diag.TextList(n)).Text("reason", "move").Text("type", u.str(t)).In(diag.PhaseTypes)
	captureAlts(d, false)
}

// constFnParams are the unit signature's const parameters of fn type, by
// local.
func (u *unit) constFnParams(sig *types.Signature) map[LocalID]source.Symbol {
	var out map[LocalID]source.Symbol
	if sig == nil {
		return nil
	}
	for i, p := range sig.Params {
		if p.Const && u.tab.Kind(p.Type) == types.KFn {
			if out == nil {
				out = map[LocalID]source.Symbol{}
			}
			out[LocalID(i+1)] = p.Name
		}
	}
	return out
}

// constFnCallKey identifies one const fn parameter passed in one call so a
// parameter cannot be forwarded to two const parameters of the same call.
type constFnCallKey struct {
	call  ast.NodeID
	param LocalID
}

// judgeConstFnUses checks the uses of const fn parameters over a body: a
// const parameter of fn type may only be called (not by s.go) or passed on
// as the whole argument of a const parameter of a call that is not s.go's,
// at most once per call; every other use is E0705 with the use's kind. A
// use inside a literal is a capture, E0705 captured (literalCaptures).
func (u *unit) judgeConstFnUses() {
	params := u.constFnParams(u.b.Sig)
	if len(params) == 0 {
		return
	}
	seenInCall := map[constFnCallKey]bool{}
	var stack []ast.Node
	ast.Inspect(u.b.Node, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if _, ok := n.(*ast.FnLit); ok && n != u.b.Node {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			if use := u.useOf(id.ID()); use.Kind == UseLocal {
				if name, ok := params[use.Local]; ok {
					u.judgeConstFnUse(id, use.Local, name, stack, seenInCall)
				}
			}
		}
		stack = append(stack, n)
		return true
	})
}

// judgeConstFnUse judges one use id of const fn parameter name, whose
// ancestors are chain.
func (u *unit) judgeConstFnUse(id *ast.Ident, param LocalID, name source.Symbol, chain []ast.Node, seenInCall map[constFnCallKey]bool) {
	var child ast.Node = id
	i := len(chain) - 1
	for ; i >= 0; i-- {
		if _, ok := chain[i].(*ast.ParenExpr); !ok {
			break
		}
		child = chain[i]
	}
	use := "value"
	if i >= 0 {
		var grand ast.Node
		if i > 0 {
			grand = chain[i-1]
		}
		switch x := chain[i].(type) {
		case *ast.CallExpr:
			if x.Fun == child {
				if u.goCalls != nil && u.goCalls[x.ID()] {
					use = "spawn"
					break
				}
				return // a call
			}
		case *ast.Arg:
			if u.constLit.Has(id.ID(), u.b.Base) {
				if c, ok := grand.(*ast.CallExpr); ok {
					k := constFnCallKey{c.ID(), param}
					if seenInCall[k] {
						n := u.c.cfg.Interner.Str(name)
						u.err(diag.E0705, id.Span(), "`"+n+"` is a `const` parameter of `fn` type: its argument may capture `var` locals exclusively, so it cannot be passed to two `const` parameters of the same call").
							Text("param", n).Text("use", "duplicate")
						return
					}
					seenInCall[k] = true
				}
				return // passed on as a const argument
			}
			use = "argument"
			if u.constNotFn.Has(id.ID(), u.b.Base) {
				// A const parameter whose type is a type parameter
				// takes the value but no environment.
				n := u.c.cfg.Interner.Str(name)
				u.err(diag.E0705, id.Span(), "`"+n+"` is a `const` parameter of `fn` type: its argument may be a literal that captures, so it can only be called, or passed on as the argument of a `const` parameter of `fn` type, and this `const` parameter's type is not a `fn` type").
					Text("param", n).Text("use", use)
				return
			}
			if c, ok := grand.(*ast.CallExpr); ok {
				if u.goCalls != nil && u.goCalls[c.ID()] {
					use = "spawn"
				} else if u.constructs(c) {
					use = "stored"
				}
			}
		case *ast.BindStmt, *ast.AssignStmt, *ast.DestructureStmt:
			use = "bound"
		case *ast.ReturnStmt:
			use = "returned"
		case *ast.RecField, *ast.RecordLit, *ast.ArrayLit:
			use = "stored"
		case *ast.BinaryExpr:
			if x.Op == ast.TokEqEq || x.Op == ast.TokNotEq {
				use = "compared"
			}
		}
	}
	n := u.c.cfg.Interner.Str(name)
	u.err(diag.E0705, id.Span(), "`"+n+"` is a `const` parameter of `fn` type: its argument may be a literal that captures, so it can only be called, or passed on as the argument of a `const` parameter").
		Text("param", n).Text("use", use)
}

// judgeContractConstFn is judgeConstFnUses's predicate form: a requires or
// ensures predicate that names a const fn parameter of its function is
// E0705 use predicate (the predicate receives &P per named parameter, and
// would call through it).
func (u *unit) judgeContractConstFn() {
	owner := u.it.Owner
	if owner == types.NoItem {
		return
	}
	fd, ok := u.c.cfg.Items.Get(owner).Decl.(*types.FuncDecl)
	if !ok {
		return
	}
	params := u.constFnParams(&fd.Sig)
	if len(params) == 0 {
		return
	}
	ast.Inspect(u.b.Node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return n != nil
		}
		r := u.ref(id)
		if r.Kind != resolve.RefParam || types.ItemID(r.B) != owner {
			return true
		}
		if name, ok := params[LocalID(r.A)]; ok {
			nm := u.c.cfg.Interner.Str(name)
			u.err(diag.E0705, id.Span(), "a contract cannot name the `const` parameter `"+nm+"` of `fn` type: its argument may be a literal that captures").
				Text("param", nm).Text("use", "predicate")
		}
		return true
	})
}
