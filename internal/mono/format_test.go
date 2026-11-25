// Why: a `const` str parameter forwarded as Print's format must be expanded
// per instance into the runner's call, and a text that does not match its
// argument type is E0706 at the call that wrote it.
// What: TestForwardedFormat*, TestForwardedPathE0706, TestForwardedRunnerChoice and TestForwardedNoFormatStruct over fmtWorld, one
// std module "std.raw" with a small stand-in of std's formatting surface
// (Print, FmtRun, FmtPiece, FmtArg, FmtValue, raw.Of, raw.PtrCast).
// How: the expansion finds its items by the same keys as in the real std;
// the tests collect and read the instance bodies and diagnostics.

package mono

import (
	"sort"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

const fmtStd = `export struct FmtPiece {
    export lit: str
    export arg: i32
    export spec: u32
}

export struct FmtArg {
    export p: *u8
    export f: fn(*u8, u32): int
}

export FmtValue[T](p: *u8, spec: u32): int
export FmtRun(w: int, pieces: []FmtPiece, args: []FmtArg): int
export Print[A](w: int, const f: str, args: A): int
export Of[T](r: &T): *T
export PtrCast[T, U](p: *T): *U

export interface Format {
    Format(&self): int
}

Line[A](w: int, const f: str, args: A): int {
    var r := 0
    unsafe "std.raw's own functions" { r = Print(w, f, args) }
    return r
}

Info[A](w: int, const f: str, args: A): int {
    var r := 0
    unsafe "std.raw's own functions" { r = Line(w, f, args) }
    return r
}
`

// fmtWorld builds the stand-in std plus extra and its formatting Known.
func fmtWorld(t *testing.T, extra string) (*srcWorld, *types.KnownItems) {
	t.Helper()
	sw := newSrcWorldAt(t, "std.raw", true, fmtStd+extra)
	k := &types.KnownItems{Print: sw.item(t, "Print"), FmtRun: sw.item(t, "FmtRun"), FmtPiece: sw.item(t, "FmtPiece"),
		FmtArg: sw.item(t, "FmtArg"), FmtValue: sw.item(t, "FmtValue"), Format: sw.item(t, "Format")}
	// int implements Format (each hole's value must have Format); any item
	// stands for the method, which the expansion never names.
	sw.impls[implKey{k.Format, 0, types.Int}] = k.FmtValue
	return sw, k
}

// TestForwardedFormatExpands: Main passes a literal through Info to
// Line, whose Print call becomes FmtRun with the literal's piece table and
// one FmtArg per field; no Print instance remains, the bodies validate.
func TestForwardedFormatExpands(t *testing.T) {
	sw, k := fmtWorld(t, `
Main(): int {
    var r := 0
    unsafe "std.raw's own functions" { r = Info(1, "a {x:x} {{b}} {y}", (x: 7, y: 2)) }
    return r
}
`)
	in, ds := Collect(sw, sw.limitRoots(), Options{Known: k, Validate: true})
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds[0].Message)
	}
	if n := len(instancesOf(in, k.Print)); n != 0 {
		t.Fatalf("%d Print instances survived the expansion", n)
	}
	lines := instancesOf(in, sw.item(t, "Line"))
	if len(lines) != 1 {
		t.Fatalf("want one Line instance, got %d", len(lines))
	}
	b := lines[0].Body
	runs := 0
	var pieces []string
	for _, blk := range b.Blocks {
		if tm := blk.Term; tm.Kind == mir.TermCall && tm.Callee.Kind == mir.CallDirect {
			switch tm.Callee.Fn.Item {
			case k.Print:
				t.Fatalf("a Print call survived")
			case k.FmtRun:
				runs++
			}
		}
		for _, st := range blk.Stmts {
			rv := st.Rvalue
			if st.Kind != mir.StAssign || rv.Kind != mir.RvAggregate || rv.Item != k.FmtPiece {
				continue
			}
			ops := b.Operands[rv.Ops.Start:rv.Ops.End()]
			lit, ok := strBytes(sw.consts, ops[0])
			if !ok {
				t.Fatalf("piece literal is not a str constant")
			}
			pieces = append(pieces, string(lit)+"|"+itoa(int32(uint32(ops[1].Bits)))+"|"+itoa(int32(ops[2].Bits)))
		}
	}
	if runs != 1 {
		t.Fatalf("want one FmtRun call, got %d", runs)
	}
	if got, want := strings.Join(pieces, ";"), "a |0|1; {b} |1|0"; got != want {
		t.Fatalf("pieces %q, want %q", got, want)
	}
	if n := len(instancesOf(in, k.FmtValue)); n != 1 {
		t.Fatalf("want FmtValue[int] once, got %d instances", n)
	}
}

func itoa(n int32) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// TestForwardedFormatE0706AtCall: a text that does not match is E0706
// at the call that wrote it, through every forwarder, with check's facts;
// a literal written in a generic forwarder anchors there.
func TestForwardedFormatE0706AtCall(t *testing.T) {
	sw, k := fmtWorld(t, `
Twice[A](w: int, args: A): int {
    var r := 0
    unsafe "std.raw's own functions" { r = Info(w, "t {q}", args) }
    return r
}

Main(): int {
    var r := 0
    unsafe "std.raw's own functions" {
        r = Info(1, "{x:x} {nope}", (x: 255, unused: 1))
        r = Twice(2, (r: 1))
        r = Info(3, "{f:x}", (f: 1.5))
    }
    return r
}
`)
	_, ds := Collect(sw, sw.limitRoots(), Options{Known: k, Validate: true})
	var got []string
	for _, d := range ds {
		if d.Code != diag.E0706 {
			t.Fatalf("unexpected %v: %s", d.Code, d.Message)
		}
		p := sw.sm.File(d.At.File).LineCol(d.At.Start)
		fv, _ := d.FactValue("problem")
		got = append(got, itoa(int32(p.Line))+" "+fv.Str)
	}
	want := map[string]bool{
		"36 hole names no field: q":      true, // Twice's literal
		"43 hole names no field: nope":   true,
		"45 x needs an integer field: f": true,
	}
	if len(got) != len(want) {
		t.Fatalf("want %d E0706, got %q", len(want), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected E0706 %q (all: %q)", g, got)
		}
	}
}

// TestForwardedRunnerChoice: a forwarded format picks the runner and
// the value functions as lower does (fmtspec.Padded, SpecTargets): the
// instance whose text has a width calls FmtPadRun, the one without calls
// FmtRun, and only the entry a valued spec reads takes FmtSpecValue.
func TestForwardedRunnerChoice(t *testing.T) {
	sw, k := fmtWorld(t, `
export FmtSpecValue[T](p: *u8, spec: u32): int
export FmtPadRun(w: int, pieces: []FmtPiece, args: []FmtArg): int

Main(): int {
    var r := 0
    unsafe "std.raw's own functions" {
        r = Info(1, "{x:+05} {y:>5}", (x: 7, y: 2))
        r = Info(2, "{z}", (z: 3))
    }
    return r
}
`)
	in, ds := Collect(sw, sw.limitRoots(), Options{Known: k, Validate: true})
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds[0].Message)
	}
	runs, pads := 0, 0
	for _, l := range instancesOf(in, sw.item(t, "Line")) {
		for _, blk := range l.Body.Blocks {
			if tm := blk.Term; tm.Kind == mir.TermCall && tm.Callee.Kind == mir.CallDirect {
				switch tm.Callee.Fn.Item {
				case k.FmtRun:
					runs++
				case sw.item(t, "FmtPadRun"):
					pads++
				}
			}
		}
	}
	if runs != 1 || pads != 1 {
		t.Fatalf("want one FmtRun and one FmtPadRun call, got %d and %d", runs, pads)
	}
	if n := len(instancesOf(in, sw.item(t, "FmtSpecValue"))); n != 1 {
		t.Fatalf("want FmtSpecValue[int] once (x), got %d instances", n)
	}
	if n := len(instancesOf(in, k.FmtValue)); n != 1 {
		t.Fatalf("want FmtValue[int] once (y and z), got %d instances", n)
	}
}

// TestForwardedPathE0706: a forwarded text's field path is judged per
// instance as check judges a literal: a path through an unexported field,
// and one ending at a type with no Format (this world has no Format), are
// E0706 at the call that wrote the text.
func TestForwardedPathE0706(t *testing.T) {
	sw, k := fmtWorld(t, `
struct S {
    export n: int
}

struct R {
    export a: S
    hid: int
}

Main(): int {
    var r := 0
    q := R(a: S(n: 1), hid: 2)
    unsafe "std.raw's own functions" {
        r = Info(1, "{q.hid}", (q))
        r = Info(2, "{q.a}", (q))
        r = Info(3, "{q.a:<4}", (q))
    }
    return r
}
`)
	_, ds := Collect(sw, sw.limitRoots(), Options{Known: k, Validate: true})
	// In source order: Collect's order is the instances', which follows
	// their names' hashes (iface's domains, which FormatVersion names).
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].At.Start < ds[j].At.Start })
	var got []string
	for _, d := range ds {
		if d.Code != diag.E0706 {
			t.Fatalf("unexpected %v: %s", d.Code, d.Message)
		}
		fv, _ := d.FactValue("problem")
		got = append(got, fv.Str)
	}
	want := "path names no exported field: q.hid;path ends at a field without Format: q.a;path ends at a field without Format: q.a"
	if g := strings.Join(got, ";"); g != want {
		t.Fatalf("E0706 problems %q, want %q", g, want)
	}
}

// TestForwardedNoFormatStruct: a forwarded instance whose record is a
// struct with no Format expands when every hole is a path to a Format
// field; a bare hole on the struct is E0706, "field without
// Format", as check judges a literal.
func TestForwardedNoFormatStruct(t *testing.T) {
	sw, k := fmtWorld(t, `
struct Row {
    export name: int
    export qty: int
}

Main(): int {
    var r := 0
    row := Row(name: 1, qty: 2)
    unsafe "std.raw's own functions" {
        r = Info(1, "{row.name} {row.qty}", (row))
        r = Info(2, "{row}", (row))
    }
    return r
}
`)
	in, ds := Collect(sw, sw.limitRoots(), Options{Known: k, Validate: true})
	var got []string
	for _, d := range ds {
		if d.Code != diag.E0706 {
			t.Fatalf("unexpected %v: %s", d.Code, d.Message)
		}
		fv, _ := d.FactValue("problem")
		got = append(got, fv.Str)
	}
	if g := strings.Join(got, ";"); g != "field without Format: row" {
		t.Fatalf("E0706 problems %q, want the bare {row} alone", g)
	}
	runs := 0
	for _, l := range instancesOf(in, sw.item(t, "Line")) {
		for _, blk := range l.Body.Blocks {
			if tm := blk.Term; tm.Kind == mir.TermCall && tm.Callee.Kind == mir.CallDirect && tm.Callee.Fn.Item == k.FmtRun {
				runs++
			}
		}
	}
	if runs != 1 {
		t.Fatalf("%d FmtRun calls, want 1 (the paths' call expanded)", runs)
	}
}
