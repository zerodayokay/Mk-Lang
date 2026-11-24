// Why: the Swiss table's probe layout, in-place rehash and iteration order
// are invisible to ordinary map tests.
// What: probe chains and rehash at compile time, a model churn of Map, HeapMap
// and Set, and iteration order across two processes.
// How: a fixture in std/core reaches the private Table with chosen hashes; the
// churn runs against an array model written in the program.

package std

import (
	"testing"
)

// tableFixture builds tables of capacity 32 or 64 over the probe heap and
// places int keys by chosen hashes: pos in bits 7 and up (H1), tag in the
// top seven bits (H2).
const tableFixture = probeHeap + `
ChosenHash(pos: int, tag: int): u64 {
    return (Trunc[u64](pos) << 7) | (Trunc[u64](tag) << 57)
}

Claim(t: mut Table[int, int], hash: u64, k: int): int {
    i := TableClaim(t, hash)
    SlotWrite(KeyAt(t.slots, i), 0, k)
    SlotWrite(ValueAt(t.slots, i), 0, k)
    return i
}
`

// TestTableProbeChains: "Remove keeps a probe chain" (capacity 32,
// the window [5,13) full of keys whose pos is 5, K with pos 5 lands at 13;
// removing slot 6 leaves 0x80 and K is still found) and "Remove frees a
// never-full run" (only slots 5 to 8 full: removing slot 6 leaves 0xFF and
// gives one growth back, and the keys at 7 and 8 are still found).
func TestTableProbeChains(t *testing.T) {
	t.Parallel()
	expectClean(t, "std07", tableFixture+`
ChainLife(): int {
    var mem: [16384]u8 = [0; 16384]
    unsafe "fixture" {
        heap := MakeHeap(raw.DataMut(mut mem[..]), raw.Of(&ProbeHeapTable))
        a := heap.Alloc()
        var bad := 0
        var t := EmptyTable[int, int]()
        if !TableResize(mut t, a, false, 32) {
            return 90
        }
        for k in 0..16 {
            if Claim(mut t, ChosenHash(5, k + 1), k) != 5 + k {
                bad += 1
            }
        }
        kh := ChosenHash(5, 100)
        if Claim(mut t, kh, 99) != 21 {
            bad += 1
        }
        TableErase(mut t, 6)
        if CtrlAt(t.ctrl, 6) != CtrlDeleted {
            bad += 1
        }
        if TableFind(&t, kh, &99) != 21 {
            bad += 1
        }
        if TableFind(&t, ChosenHash(5, 2), &1) != -1 {
            bad += 1
        }
        for k in 2..16 {
            if TableFind(&t, ChosenHash(5, k + 1), &k) != 5 + k {
                bad += 1
            }
        }
        var u := EmptyTable[int, int]()
        if !TableResize(mut u, a, false, 32) {
            return 91
        }
        for k in 0..4 {
            if Claim(mut u, ChosenHash(5, k + 1), k) != 5 + k {
                bad += 1
            }
        }
        g := u.growth
        TableErase(mut u, 6)
        if CtrlAt(u.ctrl, 6) != CtrlEmpty || u.growth != g + 1 || u.used != 3 {
            bad += 1
        }
        if TableFind(&u, ChosenHash(5, 3), &2) != 7 || TableFind(&u, ChosenHash(5, 4), &3) != 8 {
            bad += 1
        }
        if TableFind(&u, ChosenHash(5, 2), &1) != -1 {
            bad += 1
        }
        return bad
    }
}

const ChainOk = [0; 1][ChainLife()]
`)
}

// TestInPlaceRehashKeepsEveryKey: a table of capacity 64 with 50
// keys, two thirds of them removed (tombstones and empties mixed), rehashed
// in place: the capacity is the same, no 0x80 byte is left, growth is
// cap*7/8 - used, every kept key is found with its value and no removed key
// is. Then TableReserve on a table with growth 0 and used <= cap*7/16
// rehashes in place instead of doubling.
func TestInPlaceRehashKeepsEveryKey(t *testing.T) {
	t.Parallel()
	expectClean(t, "std08", tableFixture+`
RehashCheck(t: &Table[int, int], n: int): int {
    var bad := 0
    for i in 0..t.cap {
        if CtrlAt(t.ctrl, i) == CtrlDeleted {
            bad += 1
        }
    }
    if t.growth != t.cap * 7 / 8 - t.used {
        bad += 1
    }
    for k in 0..n {
        i := TableFind(t, HashOf(&k), &k)
        if k % 3 == 0 {
            if i < 0 || *SlotRef(ValueAt(t.slots, i), 0) != k * 10 {
                bad += 1
            }
        } else if i >= 0 {
            bad += 1
        }
    }
    return bad
}

RehashLife(): int {
    var mem: [16384]u8 = [0; 16384]
    unsafe "fixture" {
        heap := MakeHeap(raw.DataMut(mut mem[..]), raw.Of(&ProbeHeapTable))
        a := heap.Alloc()
        var bad := 0
        var t := EmptyTable[int, int]()
        if !TableResize(mut t, a, false, 64) {
            return 90
        }
        n := 50
        for k in 0..n {
            i := TableClaim(mut t, HashOf(&k))
            SlotWrite(KeyAt(t.slots, i), 0, k)
            SlotWrite(ValueAt(t.slots, i), 0, k * 10)
        }
        var deleted := 0
        for k in 0..n {
            if k % 3 != 0 {
                i := TableFind(&t, HashOf(&k), &k)
                TableErase(mut t, i)
                if CtrlAt(t.ctrl, i) == CtrlDeleted {
                    deleted += 1
                }
            }
        }
        if deleted == 0 {
            bad += 1
        }
        TableRehashInPlace(mut t)
        if t.cap != 64 || t.used != 17 {
            bad += 1
        }
        bad += RehashCheck(&t, n)
        t.growth = 0
        if !TableReserve(mut t, a, false, HashOf(&1000)) || t.cap != 64 {
            bad += 1
        }
        bad += RehashCheck(&t, n)
        return bad
    }
}

const RehashOk = [0; 1][RehashLife()]
`)
}

// churnProgram runs seeded random operations over a keyspace on a Map, a
// HeapMap and a Set, checking every result against an array model, then a
// full scan; it prints "ok" and the final sizes, or the first mismatch.
const churnProgram = `Model(n: int): [512]int {
    var m: [512]int = [-1; 512]
    return m
}

Next(s: mut u64): int {
    *s = *s *% 6364136223846793005 +% 1442695040888963407
    return Trunc[int](*s >> 33)
}

ChurnMap(a: &Arena, h: &Alloc, keys: int, ops: int, seed: u64): int {
    var m := Map[int, int].New(a)
    var hm := HeapMap[int, int].New(h)
    var st := Set[int].New(a)
    var model := Model(keys)
    var s := seed
    var bad := 0
    for op in 0..ops {
        r := Next(mut s)
        k := r % keys
        what := (r / keys) % 4
        if what <= 1 {
            m.Set(k, op) else {
                bad += 1
            }
            hm.Set(h, k, op) else {
                bad += 1
            }
            _ = st.Add(k) else {
                bad += 1
                continue
            }
            model[k] = op
        } else if what == 2 {
            a1 := m.Remove(&k) else { -1 }
            a2 := hm.Remove(&k) else { -1 }
            a3 := st.Remove(&k)
            if a1 != model[k] || a2 != model[k] || a3 != (model[k] >= 0) {
                bad += 1
            }
            model[k] = -1
        } else {
            var g1 := -1
            if v := m.Get(&k) {
                g1 = *v
            }
            var g2 := -1
            if v := hm.Get(&k) {
                g2 = *v
            }
            if g1 != model[k] || g2 != model[k] || st.Has(&k) != (model[k] >= 0) {
                bad += 1
            }
        }
    }
    var live := 0
    for k in 0..keys {
        if model[k] >= 0 {
            live += 1
            if !m.Has(&k) || !st.Has(&k) || hm.Get(&k).IsNone() {
                bad += 1
            }
        }
    }
    if m.Len() != live || hm.Len() != live {
        bad += 1
    }
    var seen := 0
    var it := m.Iter()
    for e := it.Next() {
        seen += 1
        if model[*e.key] != *e.value {
            bad += 1
        }
    }
    if seen != live {
        bad += 1
    }
    for k in 0..keys {
        _ = hm.Remove(&k)
    }
    hm.Free(h)
    return bad
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var bad := 0
    for keys in [7, 8, 9, 16, 57, 64, 300, 512] {
        bad += ChurnMap(sys.arena, sys.heap.Alloc(), keys, 20000, Trunc[u64](keys) *% 2654435761)
    }
    try Print(mut out, "bad {bad}\n", (bad))
}
`

// TestTableModelChurn: 20,000 seeded inserts, removes and lookups per
// keyspace (7 to 512 keys, so tables grow, fill with tombstones and rehash)
// on Map, HeapMap and Set agree with an array model at every step, and a
// full scan and iteration find exactly the live keys (7.3's "Table model
// churn", natively).
func TestTableModelChurn(t *testing.T) {
	t.Parallel()
	wantRun(t, runProgram(t, churnProgram), 0, "bad 0\n")
}

// TestMapOrderDeterministic: a Map's iteration order is a function
// of the insertion and removal sequence only: two
// processes print the same order, and so does a second build.
func TestMapOrderDeterministic(t *testing.T) {
	t.Parallel()
	src := `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var m := Map[str, int].New(sys.arena)
    for i in 0..300 {
        k := try sys.arena.Fmt("key{i}", (i))
        try m.Set(k, i)
    }
    for i in 0..300 {
        if i % 7 == 3 {
            k := try sys.arena.Fmt("key{i}", (i))
            _ = m.Remove(&k)
        }
    }
    var it := m.Iter()
    for e := it.Next() {
        v := *e.value
        try Print(mut out, "{v} ", (v))
    }
    try Print(mut out, "\n", ())
}
`
	exe := buildNative(t, nil, src)
	first := runNative(t, exe, "")
	second := runNative(t, exe, "")
	third := runNative(t, buildNative(t, nil, src), "")
	if first.code != 0 || len(first.stdout) < 300 {
		t.Fatalf("exit %d, stdout %q, stderr %s", first.code, first.stdout, first.stderr)
	}
	if second.stdout != first.stdout || third.stdout != first.stdout {
		t.Errorf("the iteration order differs between runs:\n%s\n%s\n%s", first.stdout, second.stdout, third.stdout)
	}
}
