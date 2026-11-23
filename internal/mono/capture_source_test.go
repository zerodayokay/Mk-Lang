// Why: the hand-built tests of capture_test.go use the shapes lower emits when
// nothing forces a temporary; real lowered MIR also slots a forwarded const
// parameter when a later argument is not inert.
// What: TestForwardThroughSlotSource,
// TestNonGenericForwarderRootSource and
// TestContractLiteralThroughMonoSource, plus newSrcWorld and its
// variants, which build a world from Mk source through syntax, check and
// lower.
// How: the roots are those of the driver's instance-limit walk
// (internal/driver/walk.go limitRoots: every non-generic function), so a
// non-generic function with a const parameter is among them.

package mono

import (
	"fmt"
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/check"
	"mk/internal/diag"
	"mk/internal/iface"
	"mk/internal/lower"
	"mk/internal/mir"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/syntax"
	"mk/internal/types"
)

// srcWorld is a world whose bodies lower built from one module "m" of Mk
// source with no prelude.
type srcWorld struct {
	*world
	sm    *source.SourceMap
	units []types.ItemID
	path  string
	// checker is the checker the bodies came from (its Layout is the
	// driver's; world.Layout only sizes).
	checker *check.Checker
}

// newSrcWorld parses, resolves, checks and lowers src as module m; any
// diagnostic or ICE fails the test.
func newSrcWorld(t *testing.T, src string) *srcWorld {
	t.Helper()
	return newSrcWorldAt(t, "m", false, src)
}

// newSrcWorldAt is newSrcWorld for a module at path, a std module when std
// (so a bodiless function is an intrinsic, as std's are).
func newSrcWorldAt(t *testing.T, path string, std bool, src string) *srcWorld {
	t.Helper()
	return newSrcWorldFor(t, path, std, types.X86_64Windows, false, src)
}

// newSrcWorldFor is newSrcWorldAt checked for target; with targetValue the
// module declares TargetInfo, Arch and Os as std.core does, and the world
// gives the universe name Target its value for target, as the driver does
// (internal/driver/target.go), so check folds an arch test.
func newSrcWorldFor(t testing.TB, path string, std bool, target types.Target, targetValue bool, src string) *srcWorld {
	t.Helper()
	w := &world{in: source.NewInterner(), items: types.NewItems(), consts: types.NewConstTable(),
		bodies: map[types.ItemID]*mir.Body{}, impls: map[implKey]types.ItemID{}, onerr: map[onerrKey]bool{}}
	w.tab = types.NewTable(w.in, w.items, w.consts)
	w.h = iface.NewHasher(w.tab, w.consts, func(id types.ItemID) *mir.Body { return w.bodies[id] })
	sw := &srcWorld{world: w, sm: source.NewSourceMap(), path: path}
	file := sw.sm.Add(path+"/00.mk", []byte(src))
	f, ds := syntax.ParseFile(file, w.in, syntax.Options{AllowBodiless: std})
	sw.fail(t, "parse", ds)
	w.mod = w.items.AddModule(&types.Module{Path: path, Files: []source.FileID{file.ID}, Std: std, UnsafePermitted: std})
	rs := resolve.NewSession(&resolve.Config{Interner: w.in, Sources: sw.sm, Items: w.items})
	rm := rs.Begin(&resolve.ModuleInput{Module: w.mod, Files: []*ast.File{f}, Imports: map[string]types.ModuleID{}})
	info := rm.Finish(1)
	sw.fail(t, "resolve", info.Diags)
	rds, gds := rm.Settle()
	sw.fail(t, "resolve", append(rds, gds...))
	var known *types.KnownItems
	if targetValue {
		known = sw.addTarget(t)
	}
	cfg := &check.Config{Interner: w.in, Types: w.tab, Items: w.items, Consts: w.consts, Target: target, Known: known}
	c := check.New(cfg)
	sw.checker = c
	m := c.Signatures(&check.ModuleInput{Module: w.mod, Files: []*ast.File{f}, Info: info})
	sw.fail(t, "signatures", m.Diags)
	if targetValue {
		sw.writeTarget(t, c, known, target)
	}
	lcfg := &lower.Config{Types: w.tab, Items: w.items, Consts: w.consts, Interner: w.in, Validate: true, Sources: sw.sm, Impls: lower.ImplsOf(c), Known: known}
	for _, id := range m.Units() {
		it := w.items.Get(id)
		var body *check.Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { body = c.Check(m, id) }); ie != nil {
			t.Fatalf("check ICE in %s: %v", it.Key, ie)
		}
		sw.fail(t, "check "+it.Key.String(), body.Diags)
		var mb *mir.Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { mb = lower.Lower(lcfg, lower.FromCheck(lcfg, c, body)) }); ie != nil {
			t.Fatalf("lower ICE in %s: %v", it.Key, ie)
		}
		w.bodies[id] = mb
		sw.units = append(sw.units, id)
	}
	return sw
}

// addTarget adds the const item Target of the module's TargetInfo, with no
// value yet, and names both in the KnownItems check and lower read.
func (sw *srcWorld) addTarget(t testing.TB) *types.KnownItems {
	t.Helper()
	ti := sw.item(t, "TargetInfo")
	key := types.ItemKey(sw.path, "Target")
	tgt := sw.items.AddItem(&types.Item{Module: sw.mod, Name: source.SymTarget, Kind: types.KindConst, Exported: true,
		Span: sw.items.Get(ti).Span, Key: key, Decl: &types.ConstDecl{Type: sw.tab.Named(ti, nil)}})
	return &types.KnownItems{TargetInfo: ti, Target: tgt}
}

// writeTarget writes Target's value for target in TargetInfo's layout, each
// field matched by name and an enum field holding the tag of the variant
// named like the target's Arch or OS (the driver's buildTarget).
func (sw *srcWorld) writeTarget(t testing.TB, c *check.Checker, k *types.KnownItems, target types.Target) {
	t.Helper()
	sd := sw.items.Get(k.TargetInfo).Decl.(*types.StructDecl)
	lay := c.Layout(sw.tab.Named(k.TargetInfo, nil))
	b := make([]byte, lay.Shape.Size)
	variant := func(fl types.FieldLayout, name string) {
		ed := sw.items.Get(sw.tab.Get(fl.Type).Item).Decl.(*types.EnumDecl)
		el := c.Layout(fl.Type)
		for j, v := range ed.Variants {
			if sw.in.Str(v.Name) == name {
				types.StoreUint(b[fl.Offset+el.Tag.Offset:], int(el.Tag.Size), el.Variants[j].Discr)
				return
			}
		}
		t.Fatalf("no variant %s", name)
	}
	for i, f := range sd.Fields {
		fl := lay.Fields[i]
		switch sw.in.Str(f.Name) {
		case "arch":
			variant(fl, target.Arch.String())
		case "os":
			variant(fl, target.OS.String())
		case "pointer_bytes":
			types.StoreUint(b[fl.Offset:], 8, uint64(target.PointerBytes))
		case "vector_bits":
			types.StoreUint(b[fl.Offset:], 8, uint64(target.VectorBits))
		}
	}
	d := sw.items.Get(k.Target).Decl.(*types.ConstDecl)
	d.Value = sw.consts.Intern(types.ConstValue{Type: d.Type, Align: lay.Shape.Align, Bytes: b})
	d.HasValue = true
}

func (sw *srcWorld) fail(t testing.TB, stage string, ds []*diag.Diagnostic) {
	t.Helper()
	if len(ds) == 0 {
		return
	}
	var sb strings.Builder
	for _, d := range ds {
		p := sw.sm.File(d.At.File).LineCol(d.At.Start)
		fmt.Fprintf(&sb, "  %v@%d:%d %s\n", d.Code, p.Line, p.Col, d.Message)
	}
	t.Fatalf("%s: want no diagnostics, got:\n%s", stage, sb.String())
}

// item is the item with key m.<name>.
func (sw *srcWorld) item(t testing.TB, name string) types.ItemID {
	t.Helper()
	id, ok := sw.items.ByKey(types.ItemKey(sw.path, name))
	if !ok {
		t.Fatalf("no item %s.%s", sw.path, name)
	}
	return id
}

// limitRoots are the driver's limit roots over the module: every
// non-generic function with a body, in key order (walk.go).
func (sw *srcWorld) limitRoots() []Root {
	var roots []Root
	for _, id := range sw.units {
		it := sw.items.Get(id)
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok || it.Kind != types.KindFunc || len(fd.Sig.Generics) > 0 {
			continue
		}
		roots = append(roots, Root{Kind: RootExport, Item: id})
	}
	return roots
}

// instancesOf are the instances of item.
func instancesOf(in *Instances, item types.ItemID) []*Instance {
	var out []*Instance
	for i := range in.List {
		if in.List[i].Item == item {
			out = append(out, &in.List[i])
		}
	}
	return out
}

// envArgs counts the trailing environment arguments of every direct call of
// callee in b, one entry per call.
func (sw *srcWorld) envArgs(b *mir.Body, callee types.ItemID) []int {
	var out []int
	fd := sw.items.Get(callee).Decl.(*types.FuncDecl)
	for _, blk := range b.Blocks {
		tm := blk.Term
		if tm.Kind != mir.TermCall || tm.Callee.Kind != mir.CallDirect || tm.Callee.Fn.Item != callee {
			continue
		}
		args := b.Args[tm.Args.Start:tm.Args.End()]
		n := 0
		for _, a := range args[len(fd.Sig.Params):] {
			if mir.IsEnvType(sw.tab, b.OperandType(a.Operand)) {
				n++
			}
		}
		out = append(out, n)
	}
	return out
}

const apply2Src = `Apply2[T: Plain](xs: []T, const f: fn(&T): i32, const g: fn(&T): i32): i32 {
    var s: i32 = 0
    for x in xs {
        s += f(&x) * 10 + g(&x)
    }
    return s
}

Ap3[T: Plain](xs: []T, const f: fn(&T): i32, n: i32): i32 {
    var s: i32 = n
    for x in xs {
        s += f(&x)
    }
    return s
}

Compute(m: i32): i32 {
    return m * 100
}
`

// TestForwardThroughSlotSource: a forwarded const fn parameter that
// lower copies into a temporary, because a later argument is a fn literal
// (capturing or not) or a call, still carries its environment: the instance
// passes its hidden parameter where the literal constant now stands, and the
// literal written at the call keeps its own. A plan that saw only whole
// copies of the parameter ICEd "passes a capturing literal to
// const parameter N with no environment argument".
func TestForwardThroughSlotSource(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		envs       int // environment arguments of the call in Fwd's instance
	}{
		{"forwarded first, capturing literal after", "return Apply2(xs, f, fn(x: &T): i32 { return m })", 2},
		{"forwarded first, plain literal after", "return Apply2(xs, f, fn(x: &T): i32 { return 7 })", 1},
		{"forwarded last", "return Apply2(xs, fn(x: &T): i32 { return m }, f)", 2},
		{"a call after", "return Ap3(xs, f, Compute(m))", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := apply2Src + "\nFwd[T: Plain](xs: []T, m: i32, const f: fn(&T): i32): i32 {\n    " + tc.body + "\n}\n\n" +
				"Outer(xs: []i32): i32 {\n    k: i32 = 6\n    return Fwd(xs, 3, fn(x: &i32): i32 { return *x + k })\n}\n"
			sw := newSrcWorld(t, src)
			in, ds := sw.collect(t, Options{}, sw.limitRoots()...)
			if len(ds) != 0 {
				t.Fatalf("diagnostics: %v", ds)
			}
			fwd := instancesOf(in, sw.item(t, "Fwd"))
			if len(fwd) != 1 {
				t.Fatalf("%d instances of Fwd, want 1:\n%s", len(fwd), summary(in))
			}
			fb := fwd[0].Body
			sig := sw.items.Get(fwd[0].Item).Decl.(*types.FuncDecl).Sig
			if int(fb.ParamCount) != len(sig.Params)+1 {
				t.Fatalf("Fwd's instance has ParamCount %d, want %d (one hidden environment)", fb.ParamCount, len(sig.Params)+1)
			}
			callee := sw.item(t, "Apply2")
			if strings.Contains(tc.body, "Ap3") {
				callee = sw.item(t, "Ap3")
			}
			got := sw.envArgs(fb, callee)
			if len(got) != 1 || got[0] != tc.envs {
				t.Fatalf("calls of %s in Fwd's instance pass %v environment arguments, want [%d]", sw.items.Get(callee).Key, got, tc.envs)
			}
			// The callee's instance takes as many hidden parameters.
			ci := instancesOf(in, callee)
			if len(ci) != 1 {
				t.Fatalf("%d instances of %s, want 1:\n%s", len(ci), sw.items.Get(callee).Key, summary(in))
			}
			csig := sw.items.Get(callee).Decl.(*types.FuncDecl).Sig
			if h := int(ci[0].Body.ParamCount) - len(csig.Params); h != tc.envs {
				t.Errorf("%s's instance has %d hidden parameters, want %d", sw.items.Get(callee).Key, h, tc.envs)
			}
		})
	}
}

// TestNonGenericForwarderRootSource: the driver's limit walk takes
// every non-generic function as a root, a function with a const parameter
// included; such a root forwards a parameter no constant binds, and mono
// raised "const parameter argument is not a constant". Collect skips it, and
// its instance at the literal its caller passes threads the environment.
// A const int parameter forwarded the same way is the same case.
func TestNonGenericForwarderRootSource(t *testing.T) {
	src := `Ap(xs: []i32, const f: fn(&i32): i32): i32 {
    var s: i32 = 0
    for x in xs {
        s += f(&x)
    }
    return s
}

Fwd(xs: []i32, const f: fn(&i32): i32): i32 {
    return Ap(xs, f) + Ap(xs, f)
}

G(const n: int): int {
    return n * 2
}

F(const n: int): int {
    return G(n) + 1
}

Outer(xs: []i32): int {
    k: i32 = 10
    _ = F(5)
    return Cast[int](Fwd(xs, fn(x: &i32): i32 { return *x + k }))
}
`
	sw := newSrcWorld(t, src)
	in, ds := sw.collect(t, Options{}, sw.limitRoots()...)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	fwd := instancesOf(in, sw.item(t, "Fwd"))
	if len(fwd) != 1 {
		t.Fatalf("%d instances of Fwd, want 1 (at Outer's literal, none as a root):\n%s", len(fwd), summary(in))
	}
	if n := sw.envArgs(fwd[0].Body, sw.item(t, "Ap")); len(n) != 2 || n[0] != 1 || n[1] != 1 {
		t.Errorf("Fwd's instance passes %v environment arguments to Ap, want [1 1]", n)
	}
	if f := instancesOf(in, sw.item(t, "F")); len(f) != 1 {
		t.Errorf("%d instances of F, want 1 (at 5)", len(f))
	}
	if g := instancesOf(in, sw.item(t, "G")); len(g) != 1 {
		t.Errorf("%d instances of G, want 1 (at 5)", len(g))
	}
}

// TestContractLiteralThroughMonoSource: a capturing literal in a
// requires predicate reads the function's parameter, and one in an ensures
// predicate reads result; both predicates lower with an environment built
// through their &P parameters, and the callee instance at each literal
// takes it.
func TestContractLiteralThroughMonoSource(t *testing.T) {
	src := `Any(xs: []i32, const keep: fn(&i32): bool): bool {
    for x in xs {
        if keep(&x) {
            return true
        }
    }
    return false
}

Has(xs: []i32, limit: i32): i32
    requires Any(xs, fn(x: &i32): bool { return *x > limit })
    ensures !Any(xs, fn(x: &i32): bool { return *x == result + limit })
{
    return 0
}

Outer(xs: []i32): i32 {
    return Has(xs, 3)
}
`
	sw := newSrcWorld(t, src)
	in, ds := sw.collect(t, Options{}, sw.limitRoots()...)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	anys := instancesOf(in, sw.item(t, "Any"))
	if len(anys) != 2 {
		t.Fatalf("%d instances of Any, want 2 (one per predicate literal):\n%s", len(anys), summary(in))
	}
	asig := sw.items.Get(sw.item(t, "Any")).Decl.(*types.FuncDecl).Sig
	for _, a := range anys {
		if int(a.Body.ParamCount) != len(asig.Params)+1 {
			t.Errorf("instance %s has ParamCount %d, want %d", a.Key.Text, a.Body.ParamCount, len(asig.Params)+1)
		}
	}
	// Each predicate passes its literal's environment to Any, built from its
	// own &P parameters (borrowed through, lower's environment).
	owner := sw.items.Get(sw.item(t, "Has")).Key
	for _, k := range []types.Key{types.ContractKey(owner, "requires", 0), types.ContractKey(owner, "ensures", 0)} {
		id, ok := sw.items.ByKey(k)
		if !ok {
			t.Fatalf("no predicate %s", k)
		}
		pi := instancesOf(in, id)
		if len(pi) != 1 {
			t.Fatalf("%d instances of %s, want 1", len(pi), k)
		}
		if n := sw.envArgs(pi[0].Body, sw.item(t, "Any")); len(n) != 1 || n[0] != 1 {
			t.Errorf("%s passes %v environment arguments to Any, want [1]", k, n)
		}
	}
}

// TestMutCaptureForwardingSource: a mut-capturing literal passed to a const fn
// parameter called repeatedly in a loop and forwarded across another const fn
// threads its hidden mut R environment parameter with ArgReborrow, and every
// monomorphized body validates cleanly.
func TestMutCaptureForwardingSource(t *testing.T) {
	src := `Each(xs: []i32, const f: fn(&i32)) {
    for x in xs {
        f(&x)
    }
}

Forward(xs: []i32, const f: fn(&i32)) {
    Each(xs, f)
    Each(xs, f)
}

Sum(xs: []i32, scale: i32): i32 {
    var total: i32 = 0
    Forward(xs, fn(x: &i32) {
        total += *x * scale
    })
    return total
}
`
	sw := newSrcWorld(t, src)
	in, ds := sw.collect(t, Options{}, sw.limitRoots()...)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	fwd := instancesOf(in, sw.item(t, "Forward"))
	if len(fwd) != 1 {
		t.Fatalf("%d instances of Forward, want 1", len(fwd))
	}
	// Forward takes a mut R hidden parameter and threads it to both Each calls
	// via ArgReborrow.
	fb := fwd[0].Body
	if fb.ParamCount != 3 || sw.tab.Kind(fb.Locals[3].Type) != types.KMut || !mir.IsEnvType(sw.tab, fb.Locals[3].Type) {
		t.Fatalf("Forward hidden param: ParamCount=%d type=%s", fb.ParamCount, sw.tab.TypeString(fb.Locals[3].Type, nil))
	}
	reborrows := 0
	for _, blk := range fb.Blocks {
		tm := blk.Term
		if tm.Kind != mir.TermCall || tm.Callee.Kind != mir.CallDirect || tm.Callee.Fn.Item != sw.item(t, "Each") {
			continue
		}
		args := fb.Args[tm.Args.Start:tm.Args.End()]
		if len(args) != 3 || args[2].Mode != mir.ArgReborrow {
			t.Errorf("Forward -> Each call args = %+v, want 3 args with ArgReborrow env", args)
		}
		reborrows++
	}
	if reborrows != 2 {
		t.Errorf("saw %d forwarded mut env calls, want 2", reborrows)
	}
	each := instancesOf(in, sw.item(t, "Each"))
	if len(each) != 1 {
		t.Fatalf("%d instances of Each, want 1", len(each))
	}
	eb := each[0].Body
	called := 0
	for _, blk := range eb.Blocks {
		tm := blk.Term
		if tm.Kind == mir.TermCall && tm.Callee.Kind == mir.CallDirect && sw.items.Get(tm.Callee.Fn.Item).Kind == types.KindLiteral {
			args := eb.Args[tm.Args.Start:tm.Args.End()]
			if len(args) != 2 || args[1].Mode != mir.ArgReborrow {
				t.Errorf("Each -> literal call args = %+v, want 2 args with ArgReborrow env", args)
			}
			called++
		}
	}
	if called != 1 {
		t.Errorf("saw %d literal calls in Each, want 1", called)
	}
}
