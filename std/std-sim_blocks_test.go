// Why: std.sim is how Mk programs test crash and scheduling bugs, so its own
// determinism and its power to catch bugs must be shown.
// What: TestSTDSIM_TestBlocks, TestSTDSIM_KvCrash10k, TestSTDSIM_SameSeedSameRun
// and TestSTDSIM_APIMatchesReference.
// How: the module's blocks at dev and release; a key-value store crashed over
// 10,000 seeds must catch two planted durability bugs; one seed gives one run
// across processes; the public surface matches doc/reference.md both ways.

package std

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSTDSIM_TestBlocks: mk test --std=std.sim passes every block of every
// std/sim/tests_*.mk (split by concept: tests_sim.mk, tests_tasks.mk and
// tests_faults.mk), dev and release, no two files share a block name, and
// mk runs exactly the files' blocks.
func TestSTDSIM_TestBlocks(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("sim", "tests_*.mk"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	var names []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range testNames(string(src)) {
			if prev, ok := seen[n]; ok {
				t.Errorf("block %q is in both %s and %s", n, prev, f)
			}
			seen[n] = f
			names = append(names, n)
		}
	}
	if len(names) < 39 {
		t.Fatalf("std/sim's test files have %d blocks, want at least 39", len(names))
	}
	for _, flags := range [][]string{nil, {"--release"}} {
		root := t.TempDir()
		stdDir := copyStd(t, root, nil)
		out, _ := mkCmd(t, root, stdDir, append([]string{"test", "--std=std.sim"}, flags...)...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		res := parseStdTest(out, "std.sim")
		for _, n := range names {
			if r, ok := res[n]; !ok || !r.pass {
				t.Errorf("%v: block %q: %+v\n%s", flags, n, r, out)
			}
		}
		if len(res) != len(names) {
			t.Errorf("%v: mk test ran %d blocks, the files hold %d", flags, len(res), len(names))
		}
	}
}

// simKvProgram is the key-value store of doc/examples.md, in this
// file's own copy (the conformance tests have another): a
// write-ahead log of checked records, recovered by keeping the longest
// valid prefix and rewriting it through a synced temporary file renamed
// over the log. Each seed runs three lives on one simulated disk; the
// first two crash at a seeded point (the second may crash inside
// recovery), and every completed recovery must hold exactly the writes
// whose Put returned.
const simKvProgram = `import std.fs
import std.sim

const Keys: int = 7
const Puts: int = 24
const RecSize: int = 20

struct Store {
    wal: fs.File
    len: i64
    values: [7]u64
    has: [7]bool
}

Check(key: u64, value: u64): u32 {
    var h: u32 = 0x811C9DC5
    for k in 0..8 {
        h = (h ^ Trunc[u32](key >> Trunc[u64](k * 8))) *% 0x01000193
        h = (h ^ Trunc[u32](value >> Trunc[u64](k * 8))) *% 0x01000193
    }
    return h
}

Encode(key: u64, value: u64, out: mut [20]u8) {
    c := Check(key, value)
    for k in 0..8 {
        out[k] = Trunc[u8](key >> Trunc[u64](k * 8))
        out[8 + k] = Trunc[u8](value >> Trunc[u64](k * 8))
    }
    for k in 0..4 {
        out[16 + k] = Trunc[u8](c >> Trunc[u32](k * 8))
    }
}

U64At(b: []u8, at: int): u64 {
    var x: u64 = 0
    for k in 0..8 {
        x = x | (u64(b[at + k]) << Trunc[u64](k * 8))
    }
    return x
}

Store.Open(a: &Arena, io: Io): fs.Error|OutOfMemory!Store {
    var values: [7]u64 = [0; 7]
    var has: [7]bool = [false; 7]
    var good := 0
    b := fs.ReadBytes(a, io, "db.wal") else e {
        match e {
            fs.NotFound {
            }
            _ {
                fail e
            }
        }
        return try Store.Fresh(io, values, has, 0, b"")
    }
    for good + RecSize <= b.Len() {
        key := U64At(b, good)
        value := U64At(b, good + 8)
        var c: u32 = 0
        for k in 0..4 {
            c = c | (u32(b[good + 16 + k]) << Trunc[u32](k * 8))
        }
        if c != Check(key, value) || key >= u64(Keys) {
            break
        }
        values[Trunc[int](key)] = value
        has[Trunc[int](key)] = true
        good += RecSize
    }
    return try Store.Fresh(io, values, has, good, b[0..good])
}

Store.Fresh(io: Io, values: [7]u64, has: [7]bool, good: int, prefix: []u8): fs.Error|OutOfMemory!Store {
    var f := try fs.Create(io, "db.tmp")
    onerr f.Abandon()
    try f.WriteAll(io, prefix)
    try f.Sync(io) // PLANT-FRESH
    try fs.Rename(io, "db.tmp", "db.wal")
    return Store(wal: f, len: i64(good), values: values, has: has)
}

Store.Put(mut self, io: Io, key: u64, value: u64): fs.Error!void {
    var rec: [20]u8 = [0; 20]
    Encode(key, value, mut rec)
    n := try self.wal.WriteAt(io, self.len, rec[..])
    if n != RecSize {
        fail fs.NoSpace
    }
    try self.wal.Sync(io) // PLANT-PUT
    self.len += i64(RecSize)
    self.values[Trunc[int](key)] = value
    self.has[Trunc[int](key)] = true
}

Store.Close(self, io: Io) {
    Store(wal, ..) := self
    wal.Close(io) else {
        wal.Abandon()
    }
}

Verify(st: &Store, want: &[7]u64, wanted: &[7]bool): int {
    var bad := 0
    for k in 0..Keys {
        if wanted[k] && (!st.has[k] || st.values[k] != want[k]) {
            bad += 1
        }
        if !wanted[k] && st.has[k] {
            bad += 1
        }
    }
    return bad
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var out := sys.Stdout()
    var crashed := 0
    var bad := 0
    var seeds := 0
    for seed in 0..SEEDS {
        var s := try sim.Sim.New(sys.arena, Cast[u64](seed))
        defer s.Free()
        var a := try sys.arena.Sub()
        defer a.Free()
        var want: [7]u64 = [0; 7]
        var wanted: [7]bool = [false; 7]
        var io := s.Io()
        for life in 0..3 {
            if life < 2 {
                s.CrashAfterRandomOps()
            }
            var store := Store.Open(&a, io) else {
                crashed += 1
                io = s.Restart()
                continue
            }
            bad += Verify(&store, &want, &wanted)
            if life < 2 {
                for i in 0..Puts {
                    key := Cast[u64]((seed + i * 3 + life) % Keys)
                    value := Cast[u64](seed * 1000 + life * 100 + i)
                    store.Put(io, key, value) else {
                        crashed += 1
                        break
                    }
                    want[Trunc[int](key)] = value
                    wanted[Trunc[int](key)] = true
                }
            }
            store.Close(io)
            io = s.Restart()
        }
        seeds += 1
    }
    try Print(mut out, "seeds {seeds} crashed {crashed} bad {bad}\n", (seeds, crashed, bad))
}
`

// simRun builds src over today's std with flags and runs it; its stdout.
func simRun(t *testing.T, src string, flags ...string) string {
	t.Helper()
	r := runNative(t, buildNative(t, nil, src, flags...), "")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	return strings.ReplaceAll(r.stdout, "\r\n", "\n")
}

// TestSTDSIM_KvCrash10k: the store passes its crash test over 10,000
// seeds with crashes in most of them, and each planted durability bug (no
// sync after a log append; no sync before the recovery's rename) is found.
func TestSTDSIM_KvCrash10k(t *testing.T) {
	t.Parallel()
	prog := strings.ReplaceAll(simKvProgram, "SEEDS", "10000")
	cases := []struct {
		name, plant string
		clean       bool
	}{
		{"correct", "", true},
		{"no-sync-after-append", "    try self.wal.Sync(io) // PLANT-PUT\n", false},
		{"no-sync-before-rename", "    try f.Sync(io) // PLANT-FRESH\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := prog
			if c.plant != "" {
				if !strings.Contains(src, c.plant) {
					t.Fatalf("plant %q not in the program", c.plant)
				}
				src = strings.Replace(src, c.plant, "", 1)
			}
			out := simRun(t, src, "--release")
			var seeds, crashed, bad int
			if _, err := fmt.Sscanf(out, "seeds %d crashed %d bad %d", &seeds, &crashed, &bad); err != nil {
				t.Fatalf("output %q: %v", out, err)
			}
			if seeds != 10000 || crashed < 1000 {
				t.Errorf("seeds %d crashed %d: want 10000 seeds, most of them crashing", seeds, crashed)
			}
			if c.clean && bad != 0 {
				t.Errorf("the correct store lost or invented %d values", bad)
			}
			if !c.clean && bad == 0 {
				t.Errorf("the planted bug %s was not found", c.name)
			}
		})
	}
}

// simTraceProgram prints what a seeded run saw: random bytes, the clock
// after disk and network work, the sizes of the reads a stream came back
// in, the order of datagrams, and where a crash fell.
const simTraceProgram = `import std.fs
import std.net
import std.sim

export Main(sys: &System): OutOfMemory|WriteError|fs.Error|net.Error!void {
    var out := sys.Stdout()
    var s := try sim.Sim.New(sys.arena, SEED)
    defer s.Free()
    io := s.Io()
    var r: [8]u8 = [0; 8]
    try io.Random(mut r[..])
    for b in r {
        try Print(mut out, "{b} ", (b))
    }
    try fs.WriteAll(io, "f", "data".AsBytes())
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    addr := try sys.arena.Fmt("127.0.0.1:{p}", (p: l.Port()))
    c := try net.Dial(io, addr)
    defer c.Close()
    srv := try l.Accept(io)
    defer srv.Close()
    var msg: [3000]u8 = [1; 3000]
    for _ in 0..5 {
        try c.WriteAll(io, msg[0..600])
    }
    var buf: [3000]u8 = [0; 3000]
    var n := 0
    for n < 3000 {
        k := try srv.Read(io, mut buf[n..])
        try Print(mut out, "r{k} ", (k))
        n += k
    }
    s.CrashAfterRandomOps()
    var ops := 0
    for {
        fs.WriteAll(io, "g", "x".AsBytes()) else {
            break
        }
        ops += 1
    }
    try Print(mut out, "crash after {ops}\n", (ops))
}
`

// TestSTDSIM_SameSeedSameRun: one seed run in two processes prints the
// same trace byte for byte; another seed prints another.
func TestSTDSIM_SameSeedSameRun(t *testing.T) {
	t.Parallel()
	exe5 := buildNative(t, nil, strings.ReplaceAll(simTraceProgram, "SEED", "5"))
	exe6 := buildNative(t, nil, strings.ReplaceAll(simTraceProgram, "SEED", "6"))
	a := runNative(t, exe5, "")
	b := runNative(t, exe5, "")
	c := runNative(t, exe6, "")
	for _, r := range []nativeRun{a, b, c} {
		if r.code != 0 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
		}
	}
	if a.stdout != b.stdout {
		t.Errorf("seed 5 gave two runs:\n%s\n%s", a.stdout, b.stdout)
	}
	if a.stdout == c.stdout {
		t.Errorf("seeds 5 and 6 gave the same run:\n%s", a.stdout)
	}
	if !strings.Contains(a.stdout, "crash after") || strings.Count(a.stdout, " r") < 1 {
		t.Errorf("trace is missing its parts:\n%s", a.stdout)
	}
}

// simAPIPending are reference lines std.sim declares differently on purpose;
// a line here that std.sim declares as written is logged, so it can be dropped.
var simAPIPending = map[string]string{}

// TestSTDSIM_APIMatchesReference: std.sim's public surface is exactly
// the reference library surface's "// std.sim" lines plus the types its
// module map lists (what TestNoUnlistedPublicNames and
// TestAPIMatchesReference check elsewhere, for this module, which
// std-mem_api_test.go's userModules do not cover), and the enum blocks match line for line.
func TestSTDSIM_APIMatchesReference(t *testing.T) {
	const mod = "std.sim"
	var want []string
	blocks := map[string][]string{}
	for _, it := range parseReferenceAPI(t) {
		if it.mod != mod {
			continue
		}
		text := normalizeFor(mod, it.text, nil)
		if it.block {
			blocks[blockName(strings.TrimSuffix(text, " {"))] = it.body
			continue
		}
		if have, ok := simAPIPending[text]; ok {
			t.Logf("pending: reference %q, std.sim %q", text, have)
			text = have
		}
		want = append(want, text)
	}
	if len(want) == 0 {
		t.Fatal("the reference routes no line to std.sim")
	}
	for _, n := range []string{"Op", "Fault", "NetFaults"} {
		if _, ok := blocks[n]; !ok {
			t.Errorf("the reference has no std.sim %s block", n)
		}
	}
	types := moduleTable(t)[mod]
	decls := moduleDecls(t, mod)
	if len(decls) == 0 {
		t.Fatal("std/sim declares no export (run from the std directory)")
	}
	have := map[string]bool{}
	for _, d := range decls {
		if blockKind(d.text) || strings.HasPrefix(d.text, "type ") {
			n := blockName(d.text)
			if !slices.Contains(types, n) {
				t.Errorf("std.sim exports type %s (%s), which the module map does not list", n, d.file)
			}
			declared := d.body
			if strings.Contains(d.text, "struct ") {
				declared = slices.DeleteFunc(slices.Clone(declared), func(field string) bool {
					return !strings.HasPrefix(field, "export ")
				})
			}
			if body, ok := blocks[n]; ok && !slices.Equal(body, declared) {
				t.Errorf("std.sim's %s body %q, reference %q", n, declared, body)
			}
			continue
		}
		have[d.text] = true
		if !slices.Contains(want, d.text) {
			t.Errorf("std.sim exports a name the std.sim reference does not list:\n    export %s   (%s)", d.text, d.file)
		}
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("a line of the std.sim reference is not declared in std/sim:\n    want: export %s", w)
		}
	}
	for n := range blocks {
		found := false
		for _, d := range decls {
			if blockKind(d.text) && blockName(d.text) == n {
				found = true
			}
		}
		if !found {
			t.Errorf("the reference declares %s in std.sim, which std/sim does not", n)
		}
	}
	for _, n := range types {
		found := false
		for _, d := range decls {
			if blockKind(d.text) && blockName(d.text) == n {
				found = true
			}
		}
		if !found {
			t.Errorf("the module map names std.sim's %s, which std/sim does not declare", n)
		}
	}
}
