// Why: check asks the evaluator for const values, contract results and
// calls; each failure must be reported exactly once, by the party whose
// fault it is, with the same rendering the evaluator itself gives.
//
// What: evaluate, evaluateContract and runtimeRefusal (requests and who
// reports their failures), renderEval and stageDiag, EvalConst and
// EvalConstReads (for the driver), constPointee (E1002 for an
// interior-mutable pointee of a const).
//
// How: a fault value per request says whose a failure is. The rendering
// mirrors internal/eval/render.go (check cannot import eval), with the same
// facts: trace frames as {"name", "at"}, innermost first for E0901 and
// E0902, outermost first for E0908; items; cycle; steps; fuel.

package check

import (
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// fault says who reports a failed evaluation.
type fault uint8

const (
	// faultUse: every failure is reported at the request's site (EvalCall,
	// EvalContract, a format argument's const).
	faultUse fault = iota
	// faultConstUse: a use of a named const. Only EvalStage is the use's;
	// any other failure is the const's own and the use is poison.
	faultConstUse
	// faultOwn: the const's own evaluation (Checker.EvalConst): failures at
	// its declaration, E0909 only from the cycle's least-key const.
	faultOwn
)

// evalRequest builds one request of module m at site.
func (c *Checker) evalRequest(m types.ModuleID, what types.EvalTarget, item types.ItemID, stage types.Stage, site source.Span) *types.EvalRequest {
	fuel := uint64(types.DefaultFuel)
	if d, ok := c.cfg.Items.Get(item).Decl.(*types.ConstDecl); ok && what == types.EvalConst && d.Fuel != 0 {
		fuel = d.Fuel
	}
	return &types.EvalRequest{What: what, Item: item, Fuel: fuel, Stage: stage, Module: m, Site: site, Target: c.cfg.Target}
}

// evaluate runs req through Config.Eval, merges what it read into rec, and
// returns the value (NoConst on failure or poison) with the diagnostic the
// fault rule assigns to this caller (nil when none). A nil evaluator makes
// the evaluation E0901 refused "no-evaluator" at the site, except for a use
// of a named const, which stays silent (the const's own evaluation reports).
func (c *Checker) evaluate(req *types.EvalRequest, rec recorder, f fault) (types.ConstID, *diag.Diagnostic) {
	if c.cfg.Eval == nil {
		if f == faultConstUse {
			return types.NoConst, nil
		}
		return types.NoConst, diag.New(diag.E0901, req.Site, "compile-time evaluation needs an evaluator, and this build has none").
			Text("refused", "no-evaluator").Fact("trace", diag.ListFact())
	}
	res := c.cfg.Eval.Eval(req)
	if rec.rs != nil && res.Reads.Len() > 0 {
		rec.merge(&res.Reads)
	}
	if res.Err == nil {
		return res.Value, nil
	}
	e := res.Err
	switch f {
	case faultConstUse:
		if e.Kind != types.EvalStage {
			return types.NoConst, nil
		}
	case faultOwn:
		if e.Kind == types.EvalCycle && (len(e.Items) == 0 || e.Items[0] != req.Item) {
			return types.NoConst, nil
		}
	}
	return types.NoConst, c.renderEval(e, req)
}

// evaluateContract is evaluate for an evaluation a compile-time requires
// makes (its EvalContract, or the EvalCall of an imported method a
// same-module clause calls, methodCall), whose failures are the use's
// (faultUse), except a refusal of a run-time operation (runtimeRefusal):
// reading or writing a var global, Io, a syscall, an extern, asm or mmio
// (`requires Now() > 0` with Now reading a var). The clause is then left to
// the function's entry check, run in dev and test builds and off in
// release, with no diagnostic, and refused is true.
// Any other failure keeps its diagnostic: a panic, fuel, the stage rule, a
// cycle, a raw access the evaluator refuses (a null or out-of-bounds read,
// a bad bool or char, a dead or too large allocation), pointer bytes, and a
// build with no evaluator.
func (c *Checker) evaluateContract(req *types.EvalRequest, rec recorder) (v types.ConstID, d *diag.Diagnostic, refused bool) {
	if c.cfg.Eval == nil {
		v, d = c.evaluate(req, rec, faultUse)
		return v, d, false
	}
	res := c.cfg.Eval.Eval(req)
	if rec.rs != nil && res.Reads.Len() > 0 {
		rec.merge(&res.Reads)
	}
	switch {
	case res.Err == nil:
		return res.Value, nil, false
	case res.Err.Kind == types.EvalRefused && runtimeRefusal(res.Err.Refusal):
		return types.NoConst, nil, true
	}
	return types.NoConst, c.renderEval(res.Err, req), false
}

// runtimeRefusal reports whether r is an operation only a run of the program
// can perform: reading or writing a var global, Io, a syscall, an extern,
// asm or mmio. RefuseRawAccess and RefusePointerBytes are not: they are a
// faulty access or a pointer's address, an evaluation failure.
func runtimeRefusal(r types.Refusal) bool {
	switch r {
	case types.RefuseGlobal, types.RefuseIo, types.RefuseSyscall, types.RefuseExtern, types.RefuseAsm, types.RefuseVolatile:
		return true
	}
	return false
}

// renderEval is eval.Render's table.
func (c *Checker) renderEval(err *types.EvalError, req *types.EvalRequest) *diag.Diagnostic {
	items, in := c.cfg.Items, c.cfg.Interner
	name := func(it types.ItemID) string {
		if it == types.NoItem {
			return "..."
		}
		return in.Str(items.Get(it).Name)
	}
	frame := func(i int) diag.Fact {
		if err.Items[i] == types.NoItem {
			return diag.ObjectFact(diag.Field{Key: "name", Value: diag.TextFact("...")})
		}
		var at source.Span
		if i < len(err.Trace) {
			at = err.Trace[i]
		}
		return diag.ObjectFact(diag.Field{Key: "name", Value: diag.TextFact(name(err.Items[i]))},
			diag.Field{Key: "at", Value: diag.AtFact(at)})
	}
	trace := func(innermost bool) diag.Fact {
		fs := []diag.Fact{}
		for i := range err.Items {
			j := i
			if innermost {
				j = len(err.Items) - 1 - i
			}
			fs = append(fs, frame(j))
		}
		return diag.ListFact(fs...)
	}
	site := req.Site
	switch err.Kind {
	case types.EvalPanic:
		msg := "compile-time evaluation panicked: " + err.Panic.String()
		if err.Message != "" {
			msg += ": " + err.Message
		}
		d := diag.New(diag.E0901, site, msg).Text("panic", err.Panic.String())
		switch err.Panic {
		case types.PanicExplicit, types.PanicMust, types.PanicContract:
			d.Text("message", err.Message)
		}
		return d.Fact("trace", trace(true))
	case types.EvalRefused:
		return diag.New(diag.E0901, site, "compile-time evaluation refused an operation: "+err.Refusal.String()).
			Text("refused", err.Refusal.String()).Fact("trace", trace(true))
	case types.EvalFuel:
		fuel := req.Fuel
		if fuel == 0 {
			fuel = types.DefaultFuel
		}
		d := diag.New(diag.E0902, site, "compile-time evaluation ran out of fuel after "+strconv.FormatUint(err.Steps, 10)+" steps").
			Int("steps", int64(err.Steps)).Int("fuel", int64(fuel)).Fact("trace", trace(true))
		switch req.What {
		case types.EvalConst, types.EvalGen:
			d.Alt("raise the limit: fuel " + strconv.FormatUint(2*fuel, 10))
		default:
			d.Alt("move the evaluation into a named const with fuel")
		}
		return d
	case types.EvalStage:
		return c.stageDiag(site, err.Items, trace(false))
	case types.EvalCycle:
		var names []string
		for _, it := range err.Items {
			names = append(names, name(it))
		}
		if len(names) == 0 {
			names = []string{name(req.Item)}
		}
		chain := strings.Join(append(append([]string(nil), names...), names[0]), " -> ")
		return diag.New(diag.E0909, site, "const cycle: "+chain).Fact("cycle", diag.TextList(names...))
	case types.EvalDataFile:
		return dataFileDiag(site, err).Fact("trace", trace(true))
	}
	source.ICEf(site, "check: evaluation error kind %d", err.Kind)
	return nil
}

// stageDiag is E0908 at site for the item chain (outermost first).
func (c *Checker) stageDiag(site source.Span, chain []types.ItemID, trace diag.Fact) *diag.Diagnostic {
	var keys []string
	for _, it := range chain {
		if it != types.NoItem {
			keys = append(keys, c.cfg.Items.Get(it).Key.String())
		}
	}
	last := "a same-module item"
	if n := len(chain); n > 0 && chain[n-1] != types.NoItem {
		last = c.cfg.Interner.Str(c.cfg.Items.Get(chain[n-1]).Name)
	}
	return diag.New(diag.E0908, site, "this evaluation enters "+last+", which is in the same module").
		Fact("items", diag.TextList(keys...)).Fact("trace", trace).
		Alt("move the helper to a submodule")
}

// EvalConst evaluates a const or global of m at types.StageBodies, as its
// own unit, and renders a failure at its declaration's name: E0901,
// E0902, and E0909 when it is the least-key const of a cycle. The driver
// calls it for every const and global of m (Early ones included) after the
// module's stage-6 bodies are checked and lowered, and writes
// ConstDecl.Value and HasValue from the result; check does not. A
// failed const returns NoConst.
func (c *Checker) EvalConst(m *Module, item types.ItemID) (types.ConstID, []*diag.Diagnostic) {
	v, ds, _ := c.EvalConstReads(m, item)
	return v, ds
}

// EvalConstReads is EvalConst with the reads its value rule made
// (constPointee): ReadFields "all" and ReadShape of every named type, and
// ReadFields "all" of every error set's members, it walked in a pointee.
// The driver merges them into the module's reads, so a warm check redoes
// the rule when one of those declarations changes, in this module or
// another.
func (c *Checker) EvalConstReads(m *Module, item types.ItemID) (types.ConstID, []*diag.Diagnostic, *types.ReadSet) {
	rs := &types.ReadSet{}
	it := c.cfg.Items.Get(item)
	source.Assertf(it.Module == m.ID && (it.Kind == types.KindConst || it.Kind == types.KindGlobal), it.Span,
		"check.EvalConst: %s is not a const or global of module %d", it.Key.String(), m.ID)
	site := it.Span
	if n, _ := m.nodeOf(item); n != nil {
		switch d := n.(type) {
		case *ast.ConstDecl:
			site = d.Name.Span()
		case *ast.GlobalDecl:
			site = d.Name.Span()
		}
	}
	req := c.evalRequest(m.ID, types.EvalConst, item, types.StageBodies, site)
	v, d := c.evaluate(req, recorder{}, faultOwn)
	if d != nil {
		return types.NoConst, []*diag.Diagnostic{d}, rs
	}
	if it.Kind == types.KindConst {
		if d := c.constPointee(v, site, recorder{rs: rs}); d != nil {
			return types.NoConst, []*diag.Diagnostic{d}, rs
		}
	}
	return v, nil, rs
}

// constPointee is E1002 on a const's evaluated value: a const's relocated
// data is emitted read-only,
// so a pointee whose type can hold, by value and in any variant, a Cell or
// another interior-mutable item (Mutex, Chan, Arena, std.raw.Atomic, which
// std.atomic's types hold, Scope) or a raw pointer would be written
// through, or would let unsafe code write through, read-only memory, which
// faults. Which pointees exist is read from the value the evaluator built,
// every pointee its data relocations reach at any depth, never from the
// declared type: a `?&fs.File` that is none relocates nothing (std.proc's
// DefaultCommand). Inside a pointee the walk goes by its type (an enum's
// other variants, a none optional's payload count; a walk by value would
// be more precise). A view is not in the rule: one
// that can write holds a raw pointer, or one of the items above, which the
// walk finds. Every named type and error member the walk
// visits is recorded in rec (ReadFields "all", and ReadShape of a named
// type), so the cache redoes the rule when one of them changes. Only
// consts: a `var` global's pointee is not judged here. The
// const's own bytes are constPlain's (bounds.go).
func (c *Checker) constPointee(v types.ConstID, site source.Span, rec recorder) *diag.Diagnostic {
	if v == types.NoConst {
		return nil
	}
	k := &c.known
	interior := []types.ItemID{k.Cell, k.Mutex, k.Chan, k.SpinCore, k.Arena, k.Atomic, k.Scope}
	bad := func(t types.TypeID) bool {
		ty := c.cfg.Types.Get(t)
		switch ty.Kind {
		case types.KRawPtr:
			return true
		case types.KNamed:
			key := c.cfg.Items.Get(ty.Item).Key
			rec.fields(key, true)
			rec.shape(key)
			for _, i := range interior {
				if i != types.NoItem && ty.Item == i {
					return true
				}
			}
		case types.KErrorUnion, types.KErrorSet:
			for _, em := range c.cfg.Types.Members(ty.Errors) {
				if !em.IsParam && em.Item != types.NoItem {
					rec.fields(c.cfg.Items.Get(em.Item).Key, true)
				}
			}
		}
		return false
	}
	consts := c.cfg.Types.Consts()
	seen := map[types.ConstID]bool{v: true}
	stack := []types.ConstID{v}
	for len(stack) > 0 {
		cv := consts.Get(stack[len(stack)-1])
		stack = stack[:len(stack)-1]
		for _, r := range cv.Relocs {
			if r.Kind != types.RelocData || r.Data == types.NoConst || seen[r.Data] {
				continue
			}
			seen[r.Data] = true
			stack = append(stack, r.Data)
			pt := consts.Get(r.Data).Type
			if part, ok := c.byValuePart(pt, bad); ok {
				return diag.New(diag.E1002, site, "a const's data is read-only, so it cannot point at a value that can hold a `Cell`, `Mutex`, `Chan`, `Arena`, `Atomic`, `Scope` or a raw pointer").
					In(diag.PhaseTypes).Text("type", c.cfg.Types.TypeString(pt, nil)).Text("part", c.cfg.Types.TypeString(part, nil)).
					Alt("make it at run time, or hold a `Mutex` or an atomic inline in a `var` global")
			}
		}
	}
	return nil
}
