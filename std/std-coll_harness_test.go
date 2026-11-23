// Why: the std.coll tests need std-internal calls, which user code may not
// import.
// What: the fixtures collFixCore (std/core/tests_collfix.mk) and collFixModule
// (the module std.collfix), and collBuild, collRun and collExec.
// How: tests build native programs over a copy of std that holds the fixtures;
// std.collfix re-exports std.core's calls for user code.

package std

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// collFixCore is written to std/core/tests_collfix.mk in the copy.
const collFixCore = `// Fixture of the std-coll tests: std-internal calls, exported.

export CollFixHashStr(s: str): u64 {
    return HashOf(&s)
}

export CollFixHasherStr(s: str): u64 {
    var h := NewHasher()
    s.Hash(mut h)
    return HashFinish(mut h)
}

export CollFixHashInt(k: int): u64 {
    return HashOf(&k)
}

// CollFixHasherChunks absorbs b into a fresh Hasher in the pieces cuts
// gives (each cut an end offset, ascending), with words[i] absorbed by U64
// after piece i when i < words.Len(), then the rest of b, and finishes.
export CollFixHasherChunks(b: []u8, cuts: []int, words: []u64): u64 {
    var h := NewHasher()
    var prev := 0
    for i in 0..cuts.Len() {
        h.Bytes(b[prev..cuts[i]])
        prev = cuts[i]
        if i < words.Len() {
            h.U64(words[i])
        }
    }
    h.Bytes(b[prev..b.Len()])
    return HashFinish(mut h)
}

export CollFixHashBytes(b: []u8): u64 {
    var h := NewHasher()
    h.Bytes(b)
    return HashFinish(mut h)
}

export CollFixNow(io: Io): i64 {
    return IoNow(io).value
}

// FNV-1a over the control bytes, and over (slot, key, value) of the full
// slots: the table's exact layout, as the Go model computes it.
CollFixDigest(t: &Table[int, int]): (ctrl: u64, slots: u64) {
    var c: u64 = 14695981039346656037
    var k: u64 = 14695981039346656037
    if t.cap > 0 {
        for i in 0..t.cap + 16 {
            c = (c ^ u64(CtrlAt(t.ctrl, i))) *% 1099511628211
        }
        for i in 0..t.cap {
            if CtrlAt(t.ctrl, i) < CtrlDeleted {
                k = (k ^ Trunc[u64](i)) *% 1099511628211
                k = (k ^ Trunc[u64](*SlotRef(KeyAt(t.slots, i), 0))) *% 1099511628211
                k = (k ^ Trunc[u64](*SlotRef(ValueAt(t.slots, i), 0))) *% 1099511628211
            }
        }
    }
    return (ctrl: c, slots: k)
}

// CollFixTableRun drives a heap table of int keys with a seeded sequence of
// inserts (TableLookup, TablePlace), value updates, removes (TableFind,
// TableErase) and lookups, over the given key set, and prints the table's
// shape and digests every 61 operations and after the last. With fill > 0
// the sequence is scripted instead: insert keys 0 to fill - 1, remove the
// first 5/7 of them (tombstones where runs are long), then insert fresh keys:
// at fill = 7/8 of a capacity, growth reaches 0 with few keys live, which is
// rule 6's in-place rehash.
export CollFixTableRun(w: mut Writer, a: &Alloc, keys: []int, ops: int, seed: u64, fill: int): WriteError!void {
    var t := EmptyTable[int, int]()
    var s := seed
    var acc: u64 = 0
    nk := keys.Len()
    gone := fill * 5 / 7
    for op in 0..ops {
        s = s *% 6364136223846793005 +% 1442695040888963407
        r := Trunc[int](s >> 33)
        var k := keys[r % nk]
        var what := (r / nk) % 8
        if fill > 0 {
            if op < fill {
                k = keys[op]
                what = 0
            } else if op < fill + gone {
                k = keys[op - fill]
                what = 5
            } else {
                k = keys[op - gone]
                what = 0
            }
        }
        hash := HashOf(&k)
        if what < 5 {
            look := TableLookup(&t, hash, &k)
            if look.at < 0 {
                j := TablePlace(mut t, a, true, hash, look.free)
                if j < 0 {
                    try Print(w, "oom {op}\n", (op))
                    TableFreeArrays(&t, a)
                    return
                }
                SlotWrite(KeyAt(t.slots, j), 0, k)
                SlotWrite(ValueAt(t.slots, j), 0, op)
            } else {
                *SlotMut(ValueAt(t.slots, look.at), 0) = op
            }
        } else if what < 7 {
            i := TableFind(&t, hash, &k)
            if i >= 0 {
                TableErase(mut t, i)
            }
        } else {
            f := TableFind(&t, hash, &k)
            acc = acc *% 31 +% Trunc[u64](f + 1)
        }
        if op % 61 == 0 || op == ops - 1 {
            d := CollFixDigest(&t)
            cap := t.cap
            used := t.used
            growth := t.growth
            dc := d.ctrl
            ds := d.slots
            try Print(w, "{op} {cap} {used} {growth} {dc} {ds} {acc}\n", (op, cap, used, growth, dc, ds, acc))
        }
    }
    TableFreeArrays(&t, a)
}
`

// collFixModule is written to std/collfix/collfix.mk in the copy.
const collFixModule = `// Fixture of the std-coll tests: std.core's calls for user code.

import std.core

export HashStr(s: str): u64 {
    return core.CollFixHashStr(s)
}

export HasherStr(s: str): u64 {
    return core.CollFixHasherStr(s)
}

export HashInt(k: int): u64 {
    return core.CollFixHashInt(k)
}

export Now(io: Io): i64 {
    return core.CollFixNow(io)
}

export HashBytes(b: []u8): u64 {
    return core.CollFixHashBytes(b)
}

export HasherChunks(b: []u8, cuts: []int, words: []u64): u64 {
    return core.CollFixHasherChunks(b, cuts, words)
}

export IndexByte(s: str, c: u8, from: int): int {
    if i := core.IndexByteFrom(s, c, from) {
        return i
    }
    return -1
}

export TableRun(w: mut Writer, a: &Alloc, keys: []int, ops: int, seed: u64, fill: int): WriteError!void {
    try core.CollFixTableRun(w, a, keys, ops, seed, fill)
}
`

// collBuild copies std with the two fixtures, writes src as main.mk and
// builds it with flags; it returns the executable. A missing toolchain
// skips the test, as buildNative does.
func collBuild(t *testing.T, src string, flags ...string) string {
	t.Helper()
	return collBuildWith(t, nil, src, flags...)
}

// collBuildWith is collBuild with more files written into the copy of std,
// each keyed by its path under std/ (a new module directory is created).
func collBuildWith(t *testing.T, extra map[string]string, src string, flags ...string) string {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, map[string]string{"core/tests_collfix.mk": collFixCore})
	files := map[string]string{"collfix/collfix.mk": collFixModule}
	for k, v := range extra {
		files[k] = v
	}
	for name, text := range files {
		path := filepath.Join(stdDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "main")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	args := append([]string{"build"}, flags...)
	args = append(args, "-o", exe, "main.mk")
	out, status := mkCmd(t, root, stdDir, args...)
	if status != 0 {
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		t.Fatalf("mk build: exit %d\n%s", status, out)
	}
	return exe
}

// collTimeout bounds every run: a table bug can loop for ever (a probe
// that finds no empty slot), and a hung test would hold the WSL lock.
const collTimeout = 120 * time.Second

// collRun builds src and runs it with no input and collTimeout; a nonzero
// exit or a timeout fails.
func collRun(t *testing.T, src string, flags ...string) string {
	t.Helper()
	return collExec(t, collBuild(t, src, flags...))
}

// collExec runs exe with no input and collTimeout; a nonzero exit or a
// timeout fails.
func collExec(t *testing.T, exe string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), collTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Dir = filepath.Dir(exe)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("timed out after %v\nstdout so far:\n%.2000s", collTimeout, out.String())
	}
	if err != nil {
		t.Fatalf("%v\nstdout:\n%.4000s\nstderr:\n%s", err, out.String(), errb.String())
	}
	return out.String()
}

// diffLines fails the test at the first line where got and want differ.
func diffLines(t *testing.T, got, want string) {
	t.Helper()
	g := strings.Split(got, "\n")
	w := strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			t.Fatalf("line %d differs:\n got: %q\nwant: %q", i+1, a, b)
		}
	}
}

// mkIntArray is a Mk array literal of xs.
func mkIntArray(xs []int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(x))
	}
	b.WriteByte(']')
	return b.String()
}
