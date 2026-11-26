// Why: codegen lowers std's bodiless declarations itself, so its table and
// std's declarations must agree exactly.
// What: TestIntrinsicTableMatchesStd (Intrinsics() against the declarations
// std makes, std being the ground truth), TestIntrinsicsCompile (every
// run-time intrinsic family compiles), TestSupportRTBodies (CallMain,
// ErrorName, ProfileTable, FuncTable) and TestErrorNameDisplay.
// How: the std files are parsed for bodiless declarations and their
// normalized signatures are compared with intrinsicSigs in both directions.

package codegen

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// intrinsicFiles are the std files that declare run-time intrinsics, and the
// module path of each (std/sys/<os> is the module std.sys; the Nutshell
// variant declares Linux's Syscall again, so its signature is pinned too).
var intrinsicFiles = map[string]string{
	"raw/raw.mk": "std.raw", "rt/intrinsics.mk": "std.rt", "rt/backtrace.mk": "std.rt", "sys/linux/syscall.mk": "std.sys",
	"sys/nutshell/syscall.mk": "std.sys", "sys/windows/teb.mk": "std.sys", "task/context.mk": "std.task", "core/core_slice.mk": "std.core",
	"core/core_str.mk": "std.core", "core/io_io.mk": "std.core", "dyn/erased/erased.mk": "std.dyn.erased",
	// The intrinsics of std.math, std.mem, std.runtime and std.atomic.
	"math/intrinsics.mk": "std.math", "mem/intrinsics.mk": "std.mem", "runtime/cycles.mk": "std.runtime", "runtime/hints.mk": "std.runtime",
	"atomic/pause.mk": "std.atomic",
	// The trapped call (trapform.go): std.sys.TrapEntry.
	"sys/windows/fault.mk": "std.sys",
	// dl.Present and std.sys's LazyPresent (present.go).
	"dl/dl.mk": "std.dl", "sys/windows/lazy.mk": "std.sys",
}

// notYet are std intrinsics codegen does not implement yet (none).
var notYet = map[string]bool{}

// TestIntrinsicTableMatchesStd: the keys of Intrinsics() are exactly the
// bodiless declarations of intrinsicFiles, and each key's normalized std
// signature equals intrinsicSigs', in both directions.
func TestIntrinsicTableMatchesStd(t *testing.T) {
	decl := regexp.MustCompile(`^(?:export )?([A-Za-z][A-Za-z0-9]*)(?:\[[^\]]*\])?(?:\.([A-Z][A-Za-z0-9]*))?(?:\[[^\]]*\])?\(`)
	std := map[string]string{} // key -> normalized signature (normSig)
	for file, mod := range intrinsicFileSet(t) {
		f, err := os.Open(filepath.Join("..", "..", "std", filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "{") || strings.HasPrefix(line, "//") {
				continue
			}
			mm := decl.FindStringSubmatch(line)
			if mm == nil || mm[1] == "struct" || mm[1] == "type" {
				continue
			}
			key := mod + "." + mm[1]
			if mm[2] != "" {
				key = mod + "." + mm[1] + ":" + mm[2]
			}
			sig, ok := normSig(line)
			if !ok {
				t.Errorf("%s: cannot read the signature of %q", file, line)
			}
			if prev, dup := std[key]; dup && prev != sig {
				t.Errorf("%s declares %s as %s, another variant as %s", file, key, sig, prev)
			}
			std[key] = sig
		}
		f.Close()
	}
	have := map[string]bool{}
	for _, k := range Intrinsics() {
		have[k.String()] = true
	}
	var missing, extra []string
	for k := range std {
		if !have[k] && !notYet[k] {
			missing = append(missing, k)
		}
	}
	for k := range have {
		if _, ok := std[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("std declares intrinsics codegen does not implement: %v\ncodegen implements keys std does not declare: %v", missing, extra)
	}
	// Each key's std signature is the one its lowering assumes.
	if len(intrinsicSigs) != len(intrinsicKeys) {
		t.Errorf("intrinsicSigs has %d keys, intrinsicKeys %d", len(intrinsicSigs), len(intrinsicKeys))
	}
	for _, k := range intrinsicKeys {
		want, ok := intrinsicSigs[k]
		if !ok {
			t.Errorf("%s: no intrinsicSigs entry", k)
			continue
		}
		if got, ok := std[k]; ok && got != want {
			t.Errorf("%s: std declares %s, codegen lowers %s", k, got, want)
		}
	}
	ks := Intrinsics()
	for i := 1; i < len(ks); i++ {
		if ks[i-1].Compare(ks[i]) >= 0 {
			t.Fatalf("Intrinsics() not sorted and unique at %s", ks[i])
		}
	}
}

// stdModule finds or adds the std module mod.
func (e *testEnv) stdModule(mod string) types.ModuleID {
	for i := 1; i < e.items.NumModules(); i++ {
		if e.items.Module(types.ModuleID(i)).Path == mod {
			return types.ModuleID(i)
		}
	}
	return e.items.AddModule(&types.Module{Path: mod, Std: true})
}

// stdErrItem adds an error item without payload to the std module mod.
func (e *testEnv) stdErrItem(mod, name string) types.ItemID {
	key := types.ItemKey(mod, name)
	return e.items.AddItem(&types.Item{Module: e.stdModule(mod), Name: e.in.Intern(name), Kind: types.KindError,
		Key: key, Decl: &types.ErrorDecl{Code: types.ErrorCode(key)}})
}

// stdFn adds a bodiless std intrinsic item with a signature.
func (e *testEnv) stdFn(mod, owner, name string, gens int, params []types.TypeID, result types.TypeID) types.ItemID {
	mid := e.stdModule(mod)
	key := types.ItemKey(mod, name)
	if owner != "" {
		key = types.MemberKey(mod, owner, name)
	}
	sig := types.Signature{Name: e.in.Intern(name), Result: result}
	for i := 0; i < gens; i++ {
		sig.Generics = append(sig.Generics, types.GenericParam{})
	}
	for i, p := range params {
		sig.Params = append(sig.Params, types.Param{Name: e.in.Intern("a" + string(rune('a'+i))), Type: p})
	}
	id := e.items.AddItem(&types.Item{Module: mid, Name: e.in.Intern(name), Kind: types.KindFunc, Key: key,
		Decl: &types.FuncDecl{Sig: sig, Bodiless: true, Intrinsic: true}})
	e.items.Get(id).Decl.(*types.FuncDecl).Sig.Item = id
	return id
}

// callStmt appends a call of item with generic args and operands to f,
// returning the destination local.
func (f *fnb) call(item types.ItemID, gargs []types.GenericArg, dest types.TypeID, ops ...mir.Operand) mir.LocalID {
	d := f.local(dest)
	var args []mir.Arg
	for _, o := range ops {
		args = append(args, mir.Arg{Operand: o})
	}
	next := f.newBlock()
	f.term(mir.Terminator{Kind: mir.TermCall, Callee: mir.Callee{Kind: mir.CallDirect,
		Fn: mir.FnRef{Item: item, Args: f.b.AddGenericArgs(gargs...)}}, Args: f.b.AddArgs(args...),
		Dest: place(d), Target: next, Spawn: mir.NoLocal, Span: f.at(20, 1)})
	f.cur = next
	return d
}

// TestIntrinsicsCompile: the intrinsics lower to IR that clang compiles at
// -O0 and -O2 for each target listed; std.sys.Syscall, which std/sys/linux and
// std/sys/nutshell declare, is the same `syscall` asm on x86_64-linux and
// x86_64-nutshell.
func TestIntrinsicsCompile(t *testing.T) {
	for _, target := range []types.Target{types.X86_64Linux, types.X86_64Windows, types.Aarch64Linux, types.X86_64Nutshell} {
		t.Run(target.Name, func(t *testing.T) {
			e := newTestEnv()
			pI64 := e.tt.RawPtr(types.I64)
			p0 := e.tt.Param(0)
			read := e.stdFn("std.raw", "", "Read", 1, []types.TypeID{e.tt.RawPtr(p0)}, p0)
			off := e.stdFn("std.raw", "", "Offset", 1, []types.TypeID{e.tt.RawPtr(p0), types.Int}, e.tt.RawPtr(p0))
			write := e.stdFn("std.raw", "", "Write", 1, []types.TypeID{e.tt.RawPtr(p0), p0}, types.Void)
			ctz := e.stdFn("std.raw", "", "Ctz64", 0, []types.TypeID{types.U64}, types.Int)
			mul := e.stdFn("std.raw", "", "Mul128", 0, []types.TypeID{types.U64, types.U64}, e.tt.Record([]types.RecordField{
				{Name: e.in.Intern("hi"), Type: types.U64}, {Name: e.in.Intern("lo"), Type: types.U64}}))
			bits := e.stdFn("std.raw", "", "F64Bits", 0, []types.TypeID{types.F64}, types.U64)
			sp := e.stdFn("std.rt", "", "StackPointer", 0, nil, types.Uint)
			add := e.stdFn("std.raw", "Atomic", "Add", 1, []types.TypeID{pI64, p0}, p0)
			cas := e.stdFn("std.raw", "Atomic", "Cas", 1, []types.TypeID{pI64, p0, p0}, types.Bool)
			sw := e.stdFn("std.task", "", "Switch", 0, []types.TypeID{pI64, pI64}, types.Void)
			sys := e.stdFn("std.sys", "", "Syscall", 0, []types.TypeID{types.Uint, types.Uint, types.Uint, types.Uint, types.Uint, types.Uint, types.Uint}, types.Int)
			fail := e.stdFn("std.raw", "", "Fail", 0, []types.TypeID{types.U32, types.Str}, types.Never)
			sel := e.stdFn("std.raw", "", "Select", 1, []types.TypeID{types.Bool, p0, p0}, p0)
			ti := []types.GenericArg{types.TypeArg(types.I64)}
			f := e.body(e.fn("UseRaw", []types.TypeID{pI64, types.U64, types.F64}, types.I64, 0))
			q := f.call(off, ti, pI64, mir.Copy(place(1)), mir.Imm(types.Int, 3))
			v := f.call(read, ti, types.I64, mir.Copy(place(q)))
			f.call(write, ti, types.Void, mir.Copy(place(q)), mir.Copy(place(v)))
			f.call(ctz, nil, types.Int, mir.Copy(place(2)))
			f.call(mul, nil, e.tt.Record([]types.RecordField{{Name: e.in.Intern("hi"), Type: types.U64}, {Name: e.in.Intern("lo"), Type: types.U64}}),
				mir.Copy(place(2)), mir.Copy(place(2)))
			f.call(bits, nil, types.U64, mir.Copy(place(3)))
			f.call(sp, nil, types.Uint)
			f.call(add, ti, types.I64, mir.Copy(place(1)), mir.Imm(types.I64, 1))
			f.call(cas, ti, types.Bool, mir.Copy(place(1)), mir.Imm(types.I64, 1), mir.Imm(types.I64, 2))
			f.call(sw, nil, types.Void, mir.Copy(place(1)), mir.Copy(place(1)))
			f.call(sel, ti, types.I64, mir.Imm(types.Bool, 1), mir.Copy(place(v)), mir.Imm(types.I64, 7))
			uses := []types.ItemID{off, read, write, ctz, mul, bits, sp, add, cas, sw, fail, sel}
			hasSys := target.OS == types.OSLinux || target.OS == types.OSNutshell
			if hasSys {
				u := mir.Imm(types.Uint, 0)
				f.call(sys, nil, types.Int, mir.Imm(types.Uint, 39), u, u, u, u, u, u)
				uses = append(uses, sys)
			}
			f.assign(place(0), use(mir.Copy(place(v))), f.at(21, 1))
			f.ret(f.at(21, 1))
			g := e.body(e.fn("Die", nil, types.Never, 0))
			g.call(fail, nil, types.Never, mir.Imm(types.U32, 8), strConst(e, "no"))
			g.term(mir.Terminator{Kind: mir.TermUnreachable, Span: g.at(22, 1)})
			u := unitOf(e, []*mir.Body{f.b, g.b})
			for _, id := range uses {
				var args []types.GenericArg
				if len(e.items.Get(id).Decl.(*types.FuncDecl).Sig.Generics) > 0 {
					args = ti
				}
				u.Funcs = append(u.Funcs, Func{Symbol: "-", Key: e.items.Get(id).Key.String(), Item: id, Args: args, Pack: -1})
			}
			ll := emit(t, e, u, target, types.Dev)
			for _, want := range []string{"atomicrmw add", "cmpxchg", "@llvm.cttz.i64", "mul i128", "bitcast double", "@mk_switch"} {
				if !strings.Contains(ll, want) {
					t.Errorf("no %q in the emitted IR", want)
				}
			}
			if !regexp.MustCompile(`= select i1 [^
]*, !unpredictable !`).MatchString(ll) {
				t.Errorf("std.raw.Select is not a select marked !unpredictable")
			}
			if sysAsm := `asm sideeffect "syscall", "={rax},{rax},{rdi},{rsi},{rdx},{r10},{r8},{r9},~{rcx},~{r11},~{memory}"(i64 39,`; hasSys && target.Arch == types.ArchX64 && !strings.Contains(ll, sysAsm) {
				t.Errorf("std.sys.Syscall is not %s", sysAsm)
			}
			dir := t.TempDir()
			clangC(t, target, dir, "o0", ll, "-O0")
			clangC(t, target, dir, "o2", ll, "-O2")
		})
	}
}

func TestSupportRTBodies(t *testing.T) {
	for _, target := range types.Targets() {
		t.Run(target.Name, func(t *testing.T) {
			e := newTestEnv()
			// Each error is made by a body of the unit, so ErrorName names
			// it (errnames.go).
			made := []types.ItemID{e.errItem("NotFound"), e.errItem("Closed", types.I64),
				e.stdErrItem("std.core", "ParseError"), e.stdErrItem("std.fs", "NotFound")}
			sysT := e.structT("System", types.Int)
			site := e.structT("Site", types.Str, types.U32, types.U32)
			mainRes := e.structT("MainResult", types.U32, types.Int)
			row := e.structT("FuncRow", e.tt.RawPtr(types.U8), e.tt.RawPtr(site), types.U32)
			e.stdFn("std.rt", "", "CallMain", 0, []types.TypeID{e.tt.Ref(sysT)}, mainRes)
			e.stdFn("std.rt", "", "ErrorName", 0, []types.TypeID{types.U32}, types.Str)
			e.stdFn("std.rt", "", "FuncTable", 0, nil, e.tt.Slice(row))
			e.stdFn("std.rt", "", "ProfileTable", 0, nil, e.tt.Slice(row))
			nf, _ := e.items.ByKey(types.ItemKey("main", "NotFound"))
			e.fn("Main", []types.TypeID{e.tt.Ref(sysT)}, types.Int, e.tt.ErrorSet([]types.ErrorMember{{Item: nf}}))
			var b strings.Builder
			if err := EmitSupport(errorMaker(e, made...), e, Config{Target: target, RootModule: "main"}, &b); err != nil {
				t.Fatal(err)
			}
			ll := b.String()
			for _, want := range []string{"@mk.std.rt.CallMain", "@mk.std.rt.ErrorName", "@mk.std.rt.FuncTable",
				"@mk.std.rt.ProfileTable", "c\"main.NotFound\"", "c\"unknown error\"", "c\"ParseError\"", "c\"fs.NotFound\""} {
				if !strings.Contains(ll, want) {
					t.Errorf("no %s in the support pack", want)
				}
			}
			for _, bad := range []string{"std.core.ParseError", "std.fs.NotFound"} {
				if strings.Contains(ll, bad) {
					t.Errorf("ErrorName's table holds the key text %s, not the display form (R-4)", bad)
				}
			}
			clangC(t, target, t.TempDir(), "support", ll, "-O2")
		})
	}
}

// TestErrorNameDisplay: ErrorName's display form: prelude errors bare, other
// std errors as the module's last segment and name, user errors by stable
// key, so programs print "error: fs.NotFound" and "error: ParseError",
// never "error: std.fs.NotFound".
func TestErrorNameDisplay(t *testing.T) {
	e := newTestEnv()
	for _, c := range []struct {
		id   types.ItemID
		want string
	}{
		{e.stdErrItem("std.core", "OutOfMemory"), "OutOfMemory"},
		{e.stdErrItem("std.core", "ParseError"), "ParseError"},
		{e.stdErrItem("std.fs", "NotFound"), "fs.NotFound"},
		{e.stdErrItem("std.net", "Refused"), "net.Refused"},
		{e.stdErrItem("std.sys.linux", "Errno"), "linux.Errno"},
		{e.errItem("NotFound"), "main.NotFound"},
		{e.itemIn("app.util", types.ItemKey("app.util", "Bad"), "Bad", types.KindError, &types.ErrorDecl{}), "app.util.Bad"},
	} {
		it := e.items.Get(c.id)
		if got := ErrorDisplay(e.items, it); got != c.want {
			t.Errorf("%s: display %q, want %q", it.Key, got, c.want)
		}
	}
}

// intrinsicFileSet is intrinsicFiles plus every other file of std/raw:
// std.raw's crypto intrinsics live in a file per unit
// (std/raw/aes.mk, std/raw/sha256.mk), so a unit cut is a deletion.
func intrinsicFileSet(t *testing.T) map[string]string {
	out := map[string]string{}
	for f, m := range intrinsicFiles {
		out[f] = m
	}
	ms, err := filepath.Glob(filepath.Join("..", "..", "std", "raw", "*.mk"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ms {
		out["raw/"+filepath.Base(p)] = "std.raw"
	}
	return out
}
