// Why: a filter fill declared "*d into d" may store d's own loans into whatever
// d can write, which could keep a loan of a local or a freed box past its referent.
// What: escaping-fill probes and live Reader controls for TestCore_FilterPortGrowRejects.
// How: core.FilterPortGrow's Reader declares the store as "*f into f", so the
// caller sees it; core.FilterPort's fill type has no clause, so the fill does
// not convert to it (E0608).
//
// The probes' comments are written with quotes for backticks. Without the
// clause, both probes checked clean and read dead memory (5000 and 4242).

package std

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// core8GrowZzf is the first probe: the fill writes d.a, a loan of Plant's local
// short, through d.b into the caller's variable.
const core8GrowZzf = `// Probe F1: a FilterPort fill declared "*d into d"
// writes d's own loan (d.a, a reference to a local of Plant) through d.b,
// a "mut &i64" that points at the caller's variable. FilterRead calls the
// fill through the Reader's table, so no checker sees the store.
import std.core

view struct Ds {
    a: &i64
    b: mut &i64
}

DsFill(d: mut Ds, src: mut Reader, dst: mut []u8): ReadError!int *d into d {
    *d.b = d.a
    return 0
}

const DsPort = core.FilterPort[Ds].New(DsFill)

// An empty source.
struct Empty {
    n: int
}

EmptyFill(s: &Empty, io: Io, dst: mut []u8): ReadError!int {
    return 0
}

const EmptyPort = core.ReadPort[Empty].New(EmptyFill)

// Plant leaves out pointing at its local short, with no clause.
Plant(io: Io, out: mut &i64) {
    short: i64 = 777
    e := Empty(n: 0)
    var sb: [4]u8 = [0; 4]
    var src := (&EmptyPort).Reader(io, &e, mut sb[..])
    var f := core.Filtered[Ds].New(Ds(a: &short, b: out))
    var fb: [8]u8 = [0; 8]
    var r := (&DsPort).Reader(mut f, mut src, mut fb[..])
    var d: [4]u8 = [0; 4]
    _ = r.Read(mut d[..]) else {
        return
    }
}

// Clobber writes over the stack Plant used.
Clobber(k: i64): i64 {
    var x: [64]i64 = [0; 64]
    for i in 0..64 {
        x[i] = k + i64(i)
    }
    var s: i64 = 0
    for i in 0..64 {
        s += x[i]
    }
    return s
}

export Run(h: &Heap, io: Io): i64 {
    long: i64 = 1
    var p: &i64 = &long
    Plant(io, mut p)
    c := Clobber(5000)
    return *p + 0 * c
}
`

// core8GrowZzf2 is the second probe: the first with the referent in a box that Plant
// frees before Run reads through p.
const core8GrowZzf2 = `// Probe F2: F1 with the referent on the heap. The fill
// declared "*d into d" stores d.a (a box's value) through d.b into the
// caller's variable; Plant frees the box, Run reuses its memory, and *p
// reads the new box.
import std.core

view struct Ds {
    a: &i64
    b: mut &i64
}

DsFill(d: mut Ds, src: mut Reader, dst: mut []u8): ReadError!int *d into d {
    *d.b = d.a
    return 0
}

const DsPort = core.FilterPort[Ds].New(DsFill)

struct Empty {
    n: int
}

EmptyFill(s: &Empty, io: Io, dst: mut []u8): ReadError!int {
    return 0
}

const EmptyPort = core.ReadPort[Empty].New(EmptyFill)

Plant(h: &Heap, io: Io, out: mut &i64) {
    bx := Box[i64].New(h.Alloc(), 777) else {
        return
    }
    e := Empty(n: 0)
    var sb: [4]u8 = [0; 4]
    var src := (&EmptyPort).Reader(io, &e, mut sb[..])
    var f := core.Filtered[Ds].New(Ds(a: bx.Ref(), b: out))
    var fb: [8]u8 = [0; 8]
    var r := (&DsPort).Reader(mut f, mut src, mut fb[..])
    var d: [4]u8 = [0; 4]
    _ = r.Read(mut d[..]) else {
        _ = bx.Take(h.Alloc())
        return
    }
    _ = bx.Take(h.Alloc())
}

export Run(h: &Heap, io: Io): i64 {
    long: i64 = 1
    var p: &i64 = &long
    Plant(h, io, mut p)
    other := Box[i64].New(h.Alloc(), 4242) else {
        return -1
    }
    v := *p
    _ = other.Take(h.Alloc())
    return v
}
`

// core8GrowCheck plants probe as std.<mod> (std/<mod>/p.mk) in a copy of
// std and runs mk check on a program that prints <mod>.Run.
func core8GrowCheck(t *testing.T, mod, probe string) (string, int) {
	t.Helper()
	exe := mkExe(t)
	root := t.TempDir()
	stdDir := copyStd(t, root, map[string]string{mod + "/p.mk": probe})
	main := "import std." + mod + "\n\nexport Main(sys: &System): WriteError!void {\n    r := " + mod + ".Run(sys.heap, sys.io)\n    try sys.Print(\"result {r}\\n\", (r))\n}\n"
	prog := filepath.Join(root, "prog")
	if err := os.MkdirAll(prog, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prog, "main.mk"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "check", "prog")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MK_STD="+stdDir, "MK_CACHE="+filepath.Join(root, "cache"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(out.String(), "\r\n", "\n"), status
}

// TestCore_FilterPortGrowRejects: each probe is refused at
// its line with exactly the diagnostics listed; a stray E1101 or E1102
// would mean the probe module needed unsafe code.
func TestCore_FilterPortGrowRejects(t *testing.T) {
	grow := func(s string) string {
		return strings.Replace(s, "core.FilterPort[Ds]", "core.FilterPortGrow[Ds]", 1)
	}
	for _, c := range []struct {
		name, mod, probe string
		want             []string
	}{
		{"zzf over FilterPortGrow: a local's loan through the fill", "zzf", grow(core8GrowZzf), []string{
			"std/zzf/p.mk:38:14: E0505", // short stored into memory that outlives it
			"std/zzf/p.mk:38:14: E0604", // the Reader's *f into f reaches out
		}},
		{"zzf2 over FilterPortGrow: a box's loan through the fill", "zzf2", grow(core8GrowZzf2), []string{
			"std/zzf2/p.mk:38:14: E0505", // bx stored into memory that outlives it
			"std/zzf2/p.mk:38:14: E0604", // the Reader's *f into f reaches out
		}},
		{"zzf2 over FilterPort: the fill does not convert", "zzf2", core8GrowZzf2, []string{
			"std/zzf2/p.mk:17:40: E0608",
		}},
		{"zzf over FilterPort: the fill does not convert", "zzf", core8GrowZzf, []string{
			"std/zzf/p.mk:17:40: E0608",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(c.probe, "FilterPortGrow") && strings.Contains(c.name, "FilterPortGrow") {
				t.Fatal("the probe does not name FilterPortGrow")
			}
			out, status := core8GrowCheck(t, c.mod, c.probe)
			if status == 0 {
				t.Fatalf("mk check accepted the probe:\n%s", out)
			}
			for _, w := range c.want {
				if !strings.Contains(strings.ReplaceAll(out, "\\", "/"), w) {
					t.Errorf("missing %s", w)
				}
			}
			if n := strings.Count(out, ": E"); n != len(c.want) {
				t.Errorf("%d diagnostics, want %d", n, len(c.want))
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
		})
	}
}

// TestCore_FilterPortGrowBoxLoans targets both takes with a legal Reader
// construction: the fill may update a local pointer for as long as bx lives.
// Each take is judged twice: with the Reader used after it (r holds bx's
// loan directly) and with the fill's target read after it (local holds
// bx's loan only through the Reader's "*f into f" store).
func TestCore_FilterPortGrowBoxLoans(t *testing.T) {
	probe := strings.Replace(core8GrowZzf2, "core.FilterPort[Ds]", "core.FilterPortGrow[Ds]", 1)
	probe = strings.Replace(probe, "    var f := core.Filtered[Ds].New(Ds(a: bx.Ref(), b: out))",
		"    var local := *out\n    var f := core.Filtered[Ds].New(Ds(a: bx.Ref(), b: mut local))", 1)
	const take = "_ = bx.Take(h.Alloc())"
	const read = "    _ = r.Read(mut d[..]) else {\n        }"
	const peek = "    x := *local\n    _ = x"
	errTake, okTake := "        "+take+"\n        return", "    "+take+"\n}"
	for _, c := range []struct {
		name, from, to string
		want           []string // every diagnostic, as path:line:col: code
		holder         string   // the holder of bx's loan that the E0503 names
	}{
		{name: "last use precedes both takes"},
		{"error path, the Reader read after the take", errTake, "        " + take + "\n    " + read + "\n        return",
			[]string{"std/zzf2/p.mk:42:13: E0503"}, "r"},
		{"success path, the Reader read after the take", okTake, "    " + take + "\n" + read + "\n}",
			[]string{"std/zzf2/p.mk:45:9: E0503"}, "r"},
		// The read of local is E0504 as well: the store of f's loans into
		// what f writes counts f's exclusive loan of local among them.
		{"error path, the fill's target read after the take", errTake, "        " + take + "\n    " + peek + "\n        return",
			[]string{"std/zzf2/p.mk:42:13: E0503", "std/zzf2/p.mk:43:9: E0504"}, "local"},
		{"success path, the fill's target read after the take", okTake, "    " + take + "\n" + peek + "\n}",
			[]string{"std/zzf2/p.mk:45:9: E0503", "std/zzf2/p.mk:46:5: E0504"}, "local"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := probe
			if c.from != "" {
				if strings.Count(src, c.from) != 1 {
					t.Fatal("the keep-live control did not select one take")
				}
				src = strings.Replace(src, c.from, c.to, 1)
			}
			out, status := core8GrowCheck(t, "zzf2", src)
			if c.want == nil {
				if status != 0 {
					t.Fatalf("legal Reader setup was rejected:\n%s", out)
				}
				return
			}
			slashed := strings.ReplaceAll(out, "\\", "/")
			ok := status != 0 && strings.Count(out, ": E") == len(c.want) &&
				strings.Contains(out, "E0503 move-while-borrowed: bx conflicts with a live shared loan held by "+c.holder+"\n")
			for _, w := range c.want {
				ok = ok && strings.Contains(slashed, w)
			}
			if !ok {
				t.Fatalf("want exactly %q with bx's loan held by %s, exit %d:\n%s", c.want, c.holder, status, out)
			}
		})
	}
}
