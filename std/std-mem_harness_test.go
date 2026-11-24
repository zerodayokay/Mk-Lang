// Why: std must check clean on every target, keep modules' private names
// private, and stay within its public-name budget.
// What: TestStdChecksClean, TestNoCrossModulePrivateAccess,
// and TestStdPublicNameBudget.
// How: package std reaches no compiler package, so these run mk check as a
// process over a copy of std (mkcheck_test.go's helpers) or read std's text.

package std

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkTree runs mk check with args over a copy of std plus fixtures, on
// src written as main.mk, and returns the output (diagnostics uncapped).
func checkTree(t *testing.T, fixtures map[string]string, src string, args ...string) (string, int) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, fixtures)
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return mkCmd(t, root, stdDir, append(append([]string{"check", "--max-diagnostics", "0"}, args...), "main.mk")...)
}

// allModulesUses names one item of every user-importable module except
// std.derive, so that no import is unused (E0208). std.derive is a generator
// module: nothing outside a gen line can use it, and the driver checks it in
// every build (the planted case below shows it). std.linux is left out too:
// off x86_64-linux its import is E0202 target-selected, and this program is
// checked for Windows as well (TestRt_LinuxOnlyOnX64Linux checks it).
var allModulesUses = map[string]string{
	"std.fs":      "a: fs.Error",
	"std.net":     "b: net.Error",
	"std.time":    "c: time.Instant",
	"std.proc":    "d: proc.Signal",
	"std.sync":    "e: &sync.Shared[int]",
	"std.atomic":  "f: &atomic.AtomicInt",
	"std.math":    "g: f64",
	"std.coll":    "h: &coll.Bitset",
	"std.hash":    "i: hash.Crc32State",
	"std.mem":     "j: &mem.Counting",
	"std.cli":     "k: cli.Error",
	"std.log":     "l: log.Level",
	"std.runtime": "m: runtime.FaultKind",
	"std.vm":      "n: vm.Prot",
}

func allModulesProgram(t *testing.T) string {
	t.Helper()
	var imports, params []string
	for _, m := range userModules {
		if m == "std.derive" || m == "std.linux" {
			continue
		}
		use, ok := allModulesUses[m]
		if !ok {
			t.Fatalf("userModules lists %s, which allModulesUses does not use", m)
		}
		imports = append(imports, "import "+m)
		params = append(params, use)
	}
	if len(params) != len(allModulesUses) {
		t.Fatalf("allModulesUses has %d modules, userModules %d besides std.derive and std.linux", len(allModulesUses), len(params))
	}
	return strings.Join(imports, "\n") + "\n\nUse(" + strings.Join(params, ", ") + "): f64 {\n    return math.Sqrt(g)\n}\n\nexport Main(sys: &System) {\n}\n"
}

// TestStdChecksClean: mk check of every user-importable std module (and so
// of std.core, std.raw, std.rt, std.sys and std.task, which they import)
// reports nothing, for x86_64-windows and x86_64-linux.
// That covers the particular cases: no E0410 at AtomicInt, AtomicBool,
// Mutex, Io, Alloc, Heap, no E0602 at str.Split, no
// E0204 anywhere. mk check runs no clang, so the Linux target needs
// no WSL. Planted: a type error in a std.fs file and one in a std.derive
// file are each reported, so the clean run did check those bodies.
func TestStdChecksClean(t *testing.T) {
	t.Parallel()
	src := allModulesProgram(t)
	for _, target := range []string{"x86_64-windows", "x86_64-linux"} {
		out, code := checkTree(t, nil, src, "--target", target)
		if code != 0 || strings.TrimSpace(out) != "" {
			t.Errorf("mk check --target %s of every std module: exit %d\n%s", target, code, out)
		}
	}
	plant := "export ZzPlant(): int {\n    return true\n}\n"
	for _, dir := range []string{"fs", "derive"} {
		out, code := checkTree(t, map[string]string{dir + "/zz_plant.mk": plant}, src)
		found := false
		for _, l := range strings.Split(out, "\n") {
			found = found || (strings.Contains(l, "zz_plant.mk:2:") && strings.Contains(l, "E0301"))
		}
		if code == 0 || !found {
			t.Errorf("planted E0301 in std/%s/zz_plant.mk not reported (exit %d)\n%s", dir, code, out)
		}
	}
}

// privateCase is one function of a TestNoCrossModulePrivateAccess probe and the codes its body
// line may get.
type privateCase struct {
	fn   string // the function, its body on line 2 of fn
	want []string
}

// The std-internal free functions of 2.3.5 are not prelude names, so user
// code cannot name them (E0201); std.core cannot be imported (E0201 at std).
var privateNames = []privateCase{
	{"A(io: Io): Io {\n    return MakeIo(Dangling(), 0)\n}", []string{"E0201"}},
	{"B(io: Io): int {\n    return IoOpen(io, \"x\", 0).value\n}", []string{"E0201"}},
	{"C(io: Io, b: mut []u8): Writer from b {\n    return NewWriter(io, Dangling(), Dangling(), b)\n}", []string{"E0201"}},
	{"D(): time.Duration {\n    return DurationOf(5)\n}", []string{"E0201"}},
	{"E(a: &Alloc): *u8 {\n    return AllocBytes(a, 8, 8)\n}", []string{"E0201"}},
	{"F(): time.Duration {\n    return std.core.DurationOf(5)\n}", []string{"E0201"}},
}

// Private fields of prelude types: reading or building one is E0204. The
// fields are Writer.used and Duration.count (a w.len or d.nanos names no
// member at all: E0203, also a refusal).
var privateFields = []privateCase{
	{"A(io: Io): uint {\n    return io.flags\n}", []string{"E0204"}},
	{"B(w: &Writer): int {\n    return w.used\n}", []string{"E0204"}},
	{"C(w: &Writer): int {\n    return w.len\n}", []string{"E0203", "E0204"}},
	{"D(d: time.Duration): i64 {\n    return d.count\n}", []string{"E0204"}},
	{"E(h: &Heap): &Alloc {\n    return &h.own\n}", []string{"E0204"}},
	{"F(a: &Alloc): *u8 {\n    return a.ctx\n}", []string{"E0204"}},
	{"G(): time.Duration {\n    return time.Duration(count: 5)\n}", []string{"E0204"}},
	{"H(io: Io): Io {\n    return Io(table: io.table, flags: 1)\n}", []string{"E0204"}},
}

// privateProgram returns the program and, for each case, its body's line.
func privateProgram(cases []privateCase) (string, []int) {
	var b strings.Builder
	b.WriteString("import std.time\n\n")
	line := 3
	var lines []int
	for _, c := range cases {
		b.WriteString(c.fn + "\n\n")
		lines = append(lines, line+1)
		line += strings.Count(c.fn, "\n") + 2
	}
	b.WriteString("export Main(sys: &System) {\n}\n")
	return b.String(), lines
}

// TestNoCrossModulePrivateAccess: a user module that calls MakeIo,
// IoOpen, NewWriter, DurationOf (or AllocBytes) gets E0201 for each, and
// one that reads or builds a private field of Io, Writer, Duration, Heap or
// Alloc gets E0204 for each. The two probes are separate programs,
// because name errors stop a module before its types are checked. That
// std itself has no E0204 is TestStdChecksClean's.
func TestNoCrossModulePrivateAccess(t *testing.T) {
	t.Parallel()
	for name, cases := range map[string][]privateCase{"names": privateNames, "fields": privateFields} {
		src, lines := privateProgram(cases)
		out, _ := checkTree(t, nil, src)
		got := map[int][]string{}
		for _, lc := range diagCodes(out) {
			f := strings.Fields(lc)
			n := 0
			for _, ch := range f[0] {
				n = n*10 + int(ch-'0')
			}
			got[n] = append(got[n], f[1])
		}
		at := map[int]bool{}
		for i, c := range cases {
			at[lines[i]] = true
			ok := false
			for _, w := range c.want {
				ok = ok || hasCode(got[lines[i]], w)
			}
			if !ok {
				t.Errorf("%s: main.mk:%d (%s): want one of %v, have %v\n%s", name, lines[i], strings.SplitN(c.fn, "\n", 3)[1], c.want, got[lines[i]], src)
			}
		}
		for n, codes := range got {
			if !at[n] {
				t.Errorf("%s: unexpected %v at main.mk:%d\n%s", name, codes, n, out)
			}
		}
	}
}

// publicNames counts what user code can name: the prelude names, the
// members of prelude types that std.core exports, and every export of a
// user-importable module with its members; a member is an exported struct
// field, an enum variant or an interface method of an exported block.
func publicNames(t *testing.T) (total int, per map[string]int) {
	t.Helper()
	per = map[string]int{"prelude": len(PreludeNames)}
	prelude := map[string]bool{}
	for _, n := range PreludeNames {
		prelude[n] = true
	}
	members := func(d stdDecl) int {
		n := 0
		struc := strings.Contains(d.text, "struct ")
		for _, b := range d.body {
			if !struc || strings.HasPrefix(b, "export ") {
				n++
			}
		}
		return n
	}
	for _, d := range moduleDecls(t, "std.core") {
		if blockKind(d.text) {
			if prelude[blockName(d.text)] {
				per["std.core"] += members(d)
			}
			continue
		}
		if recv, _ := itemHead(d.text); recv != "" && prelude[recv] {
			per["std.core"]++
		}
	}
	for _, m := range userModules {
		for _, d := range moduleDecls(t, m) {
			per[m]++
			if blockKind(d.text) {
				per[m] += members(d)
			}
		}
	}
	for _, n := range per {
		total += n
	}
	return total, per
}

// TestStdPublicNameBudget: what user code can name stays within 1,500
// public names; the test reports the count.
func TestStdPublicNameBudget(t *testing.T) {
	total, per := publicNames(t)
	t.Logf("std: %d public names (budget 1,500): %v", total, per)
	if total > 1500 {
		t.Errorf("std has %d public names, over the 1,500 budget", total)
	}
	for _, m := range userModules {
		if per[m] == 0 {
			t.Errorf("%s exports nothing: is its directory gone?", m)
		}
	}
	if per["std.core"] == 0 {
		t.Error("no member of a prelude type found in std.core")
	}
}
