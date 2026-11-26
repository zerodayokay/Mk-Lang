// Why: every bodiless function std declares must be known to the evaluator,
// as an intrinsic or as a refusal, or compile-time code hits an ICE.
// What: tests of std/raw's keys, Slice[T].Len, and the runtime-only
// intrinsics the evaluator refuses.
// How: reads the std sources, lists their bodiless declarations by stable
// key and compares both ways with eval's intrinsic and refusal tables.

package eval

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// TestSTDRaw_EveryDeclarationHasKey: every bodiless function and method of
// std/raw has an entry in eval's intrinsic table under its stable key
// (std.raw.Name, std.raw.Owner:Name), so no compile-time evaluation of std
// code reaches the ICE "intrinsic ... has no evaluator implementation".
func TestSTDRaw_EveryDeclarationHasKey(t *testing.T) {
	// Every file of std/raw: the crypto intrinsics live in a file per unit
	// (aes.mk, sha256.mk), so removing a unit deletes a file.
	files, err := filepath.Glob("../../std/raw/*.mk")
	if err != nil || len(files) == 0 {
		t.Fatalf("no std/raw files: %v", err)
	}
	fn := regexp.MustCompile(`^export ([A-Z][A-Za-z0-9]*)(?:\[[^\]]*\])?(?:\.([A-Z][A-Za-z0-9]*))?\(`)
	n := 0
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n") {
			m := fn.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			key := "std.raw." + m[1]
			if m[2] != "" {
				key += ":" + m[2]
			}
			n++
			if _, ok := intrinsics[key]; !ok {
				t.Errorf("%s declares %s; eval has no intrinsic for it", filepath.Base(file), key)
			}
		}
	}
	if n < 40 {
		t.Errorf("found %d declarations in std/raw, want at least 40 (the pattern no longer matches?)", n)
	}
}

// TestSTDCore_SliceLen: Slice[T].Len(&self) (std/core/core_slice.mk) is the
// length word of the slice its argument points at (a []int here); a
// null receiver is refused, never a host fault.
func TestSTDCore_SliceLen(t *testing.T) {
	w := newWorld(t)
	sl := w.tab.Slice(types.Int)
	slen := w.method("std.core", "Slice", "Len", 1, types.Int, w.tab.Ref(w.tab.Slice(w.tab.Param(0))))
	fd := w.items.Get(slen).Decl.(*types.FuncDecl)
	fd.Bodiless, fd.Intrinsic = true, true
	if _, ok := intrinsics["std.core.Slice:Len"]; !ok {
		t.Fatal("no std.core.Slice:Len")
	}
	f := w.fn("lib", "L", types.Int)
	x := w.body(f, types.Int)
	a, s, r := x.local(w.array(10, types.Int)), x.local(sl), x.local(w.tab.Ref(sl))
	b1 := x.block()
	x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvRepeat, A: imm(types.Int, 3), Len: types.ConstArg{Kind: types.CAValue, Value: w.intVal(types.Int, 10)}})
	x.assign(0, lp(s), mir.Rvalue{Kind: mir.RvSlice, Place: lp(a), HasLo: true, A: imm(types.Int, 2), HasHi: true, B: imm(types.Int, 7)})
	x.assign(0, lp(r), mir.Rvalue{Kind: mir.RvRef, Place: lp(s)})
	x.callG(0, slen, []types.TypeID{types.Int}, lp(0), b1, cp(r))
	x.ret(b1)
	x.done()
	if got := w.call(w.evaluator(0), f, 0); got.Err != nil || w.intOf(got.Value) != 5 {
		t.Fatalf("Len of s[2..7] = %+v, want 5", got)
	}
	g := w.fn("lib", "N", types.Int)
	y := w.body(g, types.Int)
	rn := y.local(w.tab.RawPtr(sl))
	c1 := y.block()
	y.assign(0, lp(rn), use(imm(w.tab.RawPtr(sl), 0)))
	y.callG(0, slen, []types.TypeID{types.Int}, lp(0), c1, cp(rn))
	y.ret(c1)
	y.done()
	wantRefusal(t, w.call(w.evaluator(0), g, 0), types.RefuseRawAccess)
}

// runtimeRefusals are the runtime-only intrinsics, each with the refusal
// the evaluator gives a call of it.
var runtimeRefusals = map[string]string{
	"std.sys.Syscall":      "syscall",
	"std.sys.Clone":        "extern",
	"std.sys.Restorer":     "extern",
	"std.sys.LibcBuild":    "extern",
	"std.sys.Teb":          "extern",
	"std.task.Switch":      "extern",
	"std.task.InitContext": "extern",
	"std.rt.CallMain":      "extern",
	"std.rt.ErrorName":     "extern",
	"std.rt.ProfileTable":  "extern",
	"std.rt.FuncTable":     "extern",
	"std.rt.StackPointer":  "pointer-bytes",
	"std.rt.UsesTasks":     "extern",
	"std.dl.Present":       "extern",
	"std.sys.LazyPresent":  "extern",
}

// TestRuntimeIntrinsicsRefused: the runtime-only intrinsics
// (runtimeRefusals) against eval's refusal table, both ways: every row of
// the list is in refusals with the refusal it names, every
// entry of refusals is a row, and every bodiless declaration of the files
// that declare them (std/rt/intrinsics.mk, std/sys/linux/syscall.mk,
// std/sys/nutshell/syscall.mk, std/sys/windows/teb.mk, std/task/context.mk,
// and the Present intrinsics of std/sys/windows/lazy.mk and std/dl/dl.mk,
// generic ones included) has a refusal under its stable key, each key
// counted once (the Nutshell
// variant declares Linux's Syscall again). An unrefused one would be the
// E0000 of an unknown intrinsic.
func TestRuntimeIntrinsicsRefused(t *testing.T) {
	rows := runtimeRefusals
	for key, want := range rows {
		r, ok := refusals[key]
		switch {
		case !ok:
			t.Errorf("runtimeRefusals refuses %s (%s); eval has no refusal for it", key, want)
		case r.String() != want:
			t.Errorf("%s is refused %s, runtimeRefusals says %s", key, r, want)
		}
	}
	for key := range refusals {
		if _, ok := rows[key]; !ok {
			t.Errorf("eval refuses %s, which runtimeRefusals does not list", key)
		}
		if _, ok := intrinsics[key]; ok {
			t.Errorf("%s is both implemented and refused", key)
		}
	}
	decl := regexp.MustCompile(`^(?:export )?([A-Z][A-Za-z0-9]*)(?:\[[^\]]*\])?\(.*\)[^{]*$`)
	files := map[string]string{
		"../../std/rt/intrinsics.mk":        "std.rt.",
		"../../std/sys/linux/syscall.mk":    "std.sys.",
		"../../std/sys/nutshell/syscall.mk": "std.sys.",
		"../../std/sys/windows/teb.mk":      "std.sys.",
		"../../std/task/context.mk":         "std.task.",
		"../../std/sys/windows/lazy.mk":     "std.sys.",
		"../../std/dl/dl.mk":                "std.dl.",
	}
	seen := map[string]bool{}
	for file, mod := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n") {
			m := decl.FindStringSubmatch(strings.TrimRight(line, " \t"))
			if m == nil {
				continue
			}
			seen[mod+m[1]] = true
			if _, ok := refusals[mod+m[1]]; !ok {
				t.Errorf("%s declares the bodiless %s%s; eval does not refuse it", file, mod, m[1])
			}
		}
	}
	if len(seen) != len(rows) {
		t.Errorf("found %d bodiless declarations in the runtime files, runtimeRefusals has %d rows", len(seen), len(rows))
	}
}

// TestRuntimeIntrinsicRefusalsRun: a call of a runtime-only intrinsic
// inside an evaluation is E0901 refused with its row's refusal, never the
// ICE of an unknown intrinsic: StackPointer reads a pointer's bytes, the
// rest are extern-like.
func TestRuntimeIntrinsicRefusalsRun(t *testing.T) {
	for _, c := range []struct {
		mod, name string
		want      types.Refusal
	}{
		{"std.rt", "StackPointer", types.RefusePointerBytes},
		{"std.rt", "ErrorName", types.RefuseExtern},
		{"std.rt", "FuncTable", types.RefuseExtern},
		{"std.sys", "Teb", types.RefuseExtern},
		{"std.sys", "Clone", types.RefuseExtern},
		{"std.task", "Switch", types.RefuseExtern},
		{"std.sys", "LazyPresent", types.RefuseExtern},
		{"std.dl", "Present", types.RefuseExtern},
	} {
		w := newWorld(t)
		in := w.fn(c.mod, c.name, types.Int)
		fd := w.items.Get(in).Decl.(*types.FuncDecl)
		fd.Bodiless, fd.Intrinsic = true, true
		f := w.fn("lib", "F", types.Int)
		x := w.body(f, types.Int)
		b1 := x.block()
		x.call(0, in, lp(0), b1)
		x.ret(b1)
		x.done()
		var r types.EvalResult
		if msg := iceOf(func() { r = w.call(w.evaluator(0), f, 0) }); msg != "" {
			t.Errorf("%s.%s: ICE %q, want refused %v", c.mod, c.name, msg, c.want)
			continue
		}
		if r.Err == nil || r.Err.Kind != types.EvalRefused || r.Err.Refusal != c.want {
			t.Errorf("%s.%s: got %+v, want refused %v", c.mod, c.name, r.Err, c.want)
		}
	}
}
