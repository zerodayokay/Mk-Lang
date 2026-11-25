// Why: a team auditing panics needs every panic site of a workspace member,
// explicit and implicit, without a cap.
// What: tests of `mk query panics --member <name> [--json]`:
// the sites, usage errors, the documented schema and an EvalArena site.
// How: mkh146Workspace builds a member with one site of each kind; the tests
// compare the listing and check that doc/tools.md names every field and kind.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"mk/internal/driver"
)

// mkh146Ops reaches its sites only through a const table of fn values
// (Ops) and a const struct of fn hooks (DefaultHooks): no direct call
// leads to OpAdd, OpDiv, OpIndex, HookGet or HookFail.
const mkh146Ops = `// Sites reached only through a const fn table and a hooks struct.

OpAdd(a: int, b: int): int {
    Assert(a >= 0)
    return a + b
}

OpDiv(a: int, b: int): int {
    return a / b
}

OpIndex(a: int, b: int): int {
    xs := [1, 2, 3]
    return xs[a] + b
}

const Ops: [3]fn(int, int): int = [OpAdd, OpDiv, OpIndex]

export struct Hooks {
    export on_get: fn(?int): int
    export on_fail: fn(int): int
}

HookGet(x: ?int): int {
    return x.Must()
}

HookFail(code: int): int {
    if code > 9 {
        Panic("bad code")
    }
    return code
}

export const DefaultHooks = Hooks(on_get: HookGet, on_fail: HookFail)

export Run(op: int, a: int, b: int): int {
    return Ops[op](a, b)
}

Twice[T: Plain](xs: &[]T, i: int): T {
    return xs[i]
}

Unused(x: i32): i32 {
    return -x
}
`

// mkh146More holds one site of each other kind: a method, a generic
// function, a shift, a Cast, a str slice, a requires clause, two asserts
// in one function, a fn literal and a test block.
const mkh146More = `export struct Vm {
    export acc: int
}

Vm.Step(mut self, k: int): int {
    self.acc = self.acc * k
    return self.acc
}

Count[T: Plain](xs: &[]T, k: int): int {
    return xs.Len() * k
}

Shift(x: u64, n: u64): u64 {
    return x << n
}

Narrow(x: int): u8 {
    return Cast[u8](x)
}

Head(s: str, n: int): str {
    return s[..n]
}

Half(x: int): int requires x > 0 {
    return x / 2
}

Twice2(x: int, y: int): int {
    Assert(x > 0)
    Assert(y > 0)
    return x
}

Apply(x: int): int {
    f := fn(v: int): int {
        return v - 1
    }
    return f(x)
}

test "ops" {
    Assert(Half(4) == 2)
}
`

// mkh146Intr holds an impl method and the std intrinsics that trap at
// the call: math.DivWide, simd.Vec.FromSlice and a vector shift.
const mkh146Intr = `import std.math
import std.simd

export interface Stepper {
    Step(mut self): int
}

export struct Ctr {
    export n: int
}

impl Stepper for Ctr {
    Step(mut self): int {
        Assert(self.n < 100)
        return self.n
    }
}

Wide(hi: u64, lo: u64, d: u64): u64 {
    return math.DivWide(hi, lo, d).q
}

Load(xs: []u32, i: int, k: u64): simd.Vec[u32, 4] {
    v := simd.Vec[u32, 4].FromSlice(xs, i)
    return v << k
}
`

// mkh146Want is every site: kind, file:line:col, function, explicit,
// generic, test.
var mkh146Want = []string{
	`assert src/intr.mk:14:9 src#impl(src.Stepper for src.Ctr):Step true false false`,
	"divide src/intr.mk:20:12 Wide false false false",
	"overflow src/intr.mk:20:12 Wide false false false",
	"bounds src/intr.mk:24:10 Load false false false",
	"shift src/intr.mk:25:12 Load false false false",
	"overflow src/more.mk:6:16 Vm.Step false false false",
	"overflow src/more.mk:11:12 Count false true false",
	"shift src/more.mk:15:12 Shift false false false",
	"cast src/more.mk:19:12 Narrow false false false",
	"bounds src/more.mk:23:12 Head false false false",
	"str-boundary src/more.mk:23:12 Head false false false",
	"contract src/more.mk:26:19 Half true false false",
	"assert src/more.mk:31:5 Twice2 true false false",
	"assert src/more.mk:32:5 Twice2 true false false",
	"overflow src/more.mk:38:16 Apply (fn literal) false false false",
	`assert src/more.mk:44:5 src#test("ops") true false true`,
	"assert src/ops.mk:4:5 OpAdd true false false",
	"overflow src/ops.mk:5:12 OpAdd false false false",
	"divide src/ops.mk:9:12 OpDiv false false false",
	"bounds src/ops.mk:14:12 OpIndex false false false",
	"overflow src/ops.mk:14:12 OpIndex false false false",
	"unwrap src/ops.mk:25:12 HookGet true false false",
	"panic src/ops.mk:30:9 HookFail true false false",
	"bounds src/ops.mk:38:12 Run false false false",
	"bounds src/ops.mk:42:12 Twice false true false",
	"overflow src/ops.mk:46:12 Unused false false false",
}

func mkh146Workspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	for name, text := range map[string]string{
		"pkg.mk":          "const Package = pkg.Package(\n    name: \"ws\",\n    members: [\"eng\"],\n)\n",
		"eng/pkg.mk":      "const Package = pkg.Package(\n    name: \"eng\",\n)\n",
		"eng/src/ops.mk":  mkh146Ops,
		"eng/src/more.mk": mkh146More,
		"eng/src/intr.mk": mkh146Intr,
	} {
		p := filepath.Join(ws, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

// TestMemberPanicSites: from the workspace root, every site of the
// member is listed, those reached only through the fn table and the hooks
// struct included; the JSON is version 1, complete and past 800 bytes in
// full; a second (warm) run prints the same bytes; the text form has one
// line per site, from the member's own directory too.
func TestMemberPanicSites(t *testing.T) {
	ws := mkh146Workspace(t)
	st, out, errOut := mk("query", "panics", "--member", "eng", "--root", ws, "--json")
	if st != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", st, out, errOut)
	}
	if len(out) <= driver.QueryCapBytes {
		t.Errorf("the listing is %d bytes; the fixture is meant to pass the %d-byte cap", len(out), driver.QueryCapBytes)
	}
	var l driver.PanicSiteList
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("JSON: %v\n%s", err, out)
	}
	if l.Version != 1 || l.Query != "panics" || l.Member != "eng" || !l.Complete || len(l.Missing) != 0 || l.Functions != 22 {
		t.Errorf("header: version %d query %q member %q complete %v missing %v functions %d",
			l.Version, l.Query, l.Member, l.Complete, l.Missing, l.Functions)
	}
	var got []string
	for _, s := range l.Sites {
		got = append(got, strings.Join([]string{s.Kind, s.File + ":" + strconv.Itoa(s.Line) + ":" + strconv.Itoa(s.Col), s.Function,
			strconv.FormatBool(s.Explicit), strconv.FormatBool(s.Generic), strconv.FormatBool(s.Test)}, " "))
		if s.Module != "src" || s.Code == 0 {
			t.Errorf("site %+v: module or code", s)
		}
	}
	if !slices.Equal(got, mkh146Want) {
		t.Errorf("sites:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(mkh146Want, "\n"))
	}
	for _, key := range []string{`"version":1`, `"sites":[`, `"missing":[]`, `"explicit":`, `"generic":`, `"test":`} {
		if !strings.Contains(out, key) {
			t.Errorf("JSON lacks %s", key)
		}
	}
	if st, warm, _ := mk("query", "panics", "--member", "eng", "--root", ws, "--json"); st != 0 || warm != out {
		t.Errorf("warm run: status %d, output differs from the cold one:\n%s", st, warm)
	}
	st, text, errOut := mk("query", "panics", "--member", "eng", "--root", filepath.Join(ws, "eng"))
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if st != 0 || len(lines) != 1+len(mkh146Want) || lines[0] != "member eng: 22 function(s) read; 26 panic site(s), 8 explicit" ||
		lines[2] != "divide  src/intr.mk:20:12  in Wide" || !slices.Contains(lines, "bounds  src/ops.mk:42:12  in Twice (generic: every instance)") {
		t.Errorf("text: status %d\n%s\nstderr %s", st, text, errOut)
	}
}

// TestMemberUsage: an unknown member names the members (exit 2);
// --member with another query, or with an F, is a usage error; `panics F`
// is unchanged.
func TestMemberUsage(t *testing.T) {
	ws := mkh146Workspace(t)
	if st, _, errOut := mk("query", "panics", "--member", "nope", "--root", ws); st != 2 || !strings.Contains(errOut, "the workspace's members are eng") {
		t.Errorf("unknown member: status %d stderr %q", st, errOut)
	}
	if st, _, _ := mk("query", "panics", "OpAdd", "--member", "eng", "--root", ws); st != 2 {
		t.Errorf("panics F --member: status %d", st)
	}
	if st, _, _ := mk("query", "modules", "--member", "eng", "--root", ws); st != 2 {
		t.Errorf("modules --member: status %d", st)
	}
	if st, out, _ := mk("query", "panics", "Run", "--root", filepath.Join(ws, "eng")); st != 0 || !strings.Contains(out, "1 indirect call(s) not followed") {
		t.Errorf("panics F: status %d\n%s", st, out)
	}
}

// TestSchemaDocumented: the "Member-wide panic sites" section of
// doc/tools.md names every field of the document and every kind.
func TestSchemaDocumented(t *testing.T) {
	sec := toolsSection(t, "Member-wide panic sites")
	names := append(driver.PanicSiteKinds(), "version", "query", "member", "functions", "complete", "missing", "sites",
		"file", "line", "col", "kind", "code", "explicit", "function", "module", "generic", "test")
	for _, n := range names {
		if !strings.Contains(sec, "`"+n+"`") {
			t.Errorf("doc/tools.md's query API section does not name `%s`", n)
		}
	}
}

// TestEvalArenaSite: EvalArena() in a requires clause
// that check judges at compile time is still run by the dev entry check,
// where codegen turns it into Panic's trap (code 8); the listing names it
// at the call.
func TestEvalArenaSite(t *testing.T) {
	ws := t.TempDir()
	for name, text := range map[string]string{
		"pkg.mk": "const Package = pkg.Package(\n    name: \"ea\",\n)\n",
		"src/a.mk": "export Need(a: &Arena, n: int): bool { return n > 0 }\n" +
			"export F(const n: int): int\n    requires Need(EvalArena(), n)\n{ return n }\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, out, errOut := mk("query", "panics", "--member", "ea", "--root", ws, "--json")
	if st != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", st, out, errOut)
	}
	var l driver.PanicSiteList
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("JSON: %v\n%s", err, out)
	}
	var got []string
	for _, s := range l.Sites {
		got = append(got, s.Kind+" "+s.File+":"+strconv.Itoa(s.Line)+":"+strconv.Itoa(s.Col)+" "+s.Function+
			" "+strconv.Itoa(s.Code)+" "+strconv.FormatBool(s.Explicit))
	}
	if !l.Complete || !slices.Contains(got, "panic src/a.mk:3:19 src.F#requires0 8 true") {
		t.Errorf("complete %v; sites:\n%s", l.Complete, strings.Join(got, "\n"))
	}
}
