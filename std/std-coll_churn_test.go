// Why: a hash map under insert and remove churn must keep its memory bounded,
// not grow with the number of operations.
// What: TestStdColl_ChurnBounded over a Map on an Arena, a HeapMap, the
// cookbook Lru and a TextMap.
// How: each runs over a counting heap against fixed bounds on capacity, peak
// and live bytes; the peaks at n = 10^5 and 10^6 must be equal.

package std

import (
	"fmt"
	"strings"
	"testing"
)

const collChurnCore = `import std.raw

// A counting allocator over another: live bytes and their peak.
struct CollCount {
    inner: *Alloc
    live: int
    peak: int
}

const CollCountTable = AllocTable(alloc: CollCountAlloc, resize: CollCountResize, free: CollCountFree)

CollCountAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is a live CollCount whose inner allocator outlives it" {
        q := raw.PtrCast[u8, CollCount](ctx)
        var c := raw.Read(q)
        p := AllocBytes(&*c.inner, size, align)
        if !raw.IsNull(p) {
            c.live += size
            c.peak = IntMax(c.peak, c.live)
        }
        raw.Write(q, c)
        return p
    }
}

CollCountResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is a live CollCount whose inner allocator outlives it" {
        q := raw.PtrCast[u8, CollCount](ctx)
        var c := raw.Read(q)
        ok := ResizeBytes(&*c.inner, p, old, new, align)
        if ok {
            c.live += new - old
            c.peak = IntMax(c.peak, c.live)
        }
        raw.Write(q, c)
        return ok
    }
}

CollCountFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is a live CollCount whose inner allocator outlives it" {
        q := raw.PtrCast[u8, CollCount](ctx)
        var c := raw.Read(q)
        FreeBytes(&*c.inner, p, size, align)
        c.live -= size
        raw.Write(q, c)
    }
}

// The cookbook's Lru (04d), over HeapList.
struct CollLru {
    capacity: int
    keys: HeapList[int]
    values: HeapList[int]
}

CollLru.Put(mut self, a: &Alloc, k: int, v: int): OutOfMemory!void {
    if self.keys.Len() == self.capacity {
        _ = self.keys.Remove(0)
        _ = self.values.Remove(0)
    }
    try self.keys.Push(a, k)
    try self.values.Push(a, v)
}

CollLru.Get(&self, k: &int): ?&int from self {
    for i, x in self.keys {
        if x.Eq(k) {
            return self.values.Get(i)
        }
    }
    return none
}

CollLru.Free(self, a: &Alloc) {
    CollLru(keys, values, ..) := self
    keys.Free(a)
    values.Free(a)
}

// Churn: n inserts each followed (window keys later) by its remove, on a
// Map over an Arena and on a HeapMap, both over counting heaps; then the
// cookbook Lru of capacity 100 over n accesses. Prints the capacities, the
// largest live count and the peak bytes, and the live bytes after each
// container is freed.
export CollFixChurn(w: mut Writer, a: &Alloc, n: int, window: int, klen: int): OutOfMemory|WriteError!void {
    var c1 := CollCount(inner: raw.Of(a), live: 0, peak: 0)
    var c2 := CollCount(inner: raw.Of(a), live: 0, peak: 0)
    var c3 := CollCount(inner: raw.Of(a), live: 0, peak: 0)
    var c4 := CollCount(inner: raw.Of(a), live: 0, peak: 0)
    unsafe "each counter outlives the heap made over it, freed below" {
        h1 := MakeHeap(raw.PtrCast[CollCount, u8](raw.Of(&c1)), raw.Of(&CollCountTable))
        h2 := MakeHeap(raw.PtrCast[CollCount, u8](raw.Of(&c2)), raw.Of(&CollCountTable))
        h3 := MakeHeap(raw.PtrCast[CollCount, u8](raw.Of(&c3)), raw.Of(&CollCountTable))
        h4 := MakeHeap(raw.PtrCast[CollCount, u8](raw.Of(&c4)), raw.Of(&CollCountTable))
        var ar := try Arena.New(&h1)
        var m := Map[int, int].New(&ar)
        var max_len := 0
        var max_cap := 0
        for i in 0..n {
            m.Set(i, 0) else {
                ar.Free()
                fail OutOfMemory
            }
            max_len = IntMax(max_len, m.Len())
            max_cap = IntMax(max_cap, m.t.cap)
            if i >= window {
                old := i - window
                _ = m.Remove(&old)
            }
        }
        cap1 := m.t.cap
        ar.Free()
        b1 := raw.Read(raw.Of(&c1))
        p1 := b1.peak
        l1 := b1.live
        try Print(w, "map cap {cap1} maxcap {max_cap} maxlen {max_len} peak {p1} after {l1}\n", (cap1, max_cap, max_len, p1, l1))
        a2 := h2.Alloc()
        var hm := HeapMap[int, int].New(a2)
        max_len = 0
        max_cap = 0
        for i in 0..n {
            hm.Set(a2, i, 0) else {
                hm.Free(a2)
                fail OutOfMemory
            }
            max_len = IntMax(max_len, hm.Len())
            max_cap = IntMax(max_cap, hm.t.cap)
            if i >= window {
                old := i - window
                _ = hm.Remove(&old)
            }
        }
        cap2 := hm.t.cap
        hm.Free(a2)
        b2 := raw.Read(raw.Of(&c2))
        p2 := b2.peak
        l2 := b2.live
        try Print(w, "heapmap cap {cap2} maxcap {max_cap} maxlen {max_len} peak {p2} after {l2}\n", (cap2, max_cap, max_len, p2, l2))
        a3 := h3.Alloc()
        var lru := CollLru(capacity: 100, keys: HeapList[int].New(a3), values: HeapList[int].New(a3))
        var hits := 0
        var s: u64 = 3
        for i in 0..n {
            s = s *% 6364136223846793005 +% 1442695040888963407
            k := Trunc[int](s >> 33) % 300
            if v := lru.Get(&k) {
                hits += *v - *v + 1
            } else {
                lru.Put(a3, k, i) else {
                    lru.Free(a3)
                    fail OutOfMemory
                }
            }
        }
        lru.Free(a3)
        b3 := raw.Read(raw.Of(&c3))
        p3 := b3.peak
        l3 := b3.live
        try Print(w, "lru hits {hits} peak {p3} after {l3}\n", (hits, p3, l3))
        // TextMap: the same churn with keys of klen digits: 8 stay in their
        // slots; 16 are each copied into the heap on insert and freed on
        // remove.
        a4 := h4.Alloc()
        var tm := TextMap[int].New(a4)
        var buf: [16]u8 = [0; 16]
        max_len = 0
        max_cap = 0
        for i in 0..n {
            k := CollKey(mut buf, i, klen)
            _ = tm.Ensure(a4, k, i) else {
                tm.Free(a4)
                fail OutOfMemory
            }
            max_len = IntMax(max_len, tm.Len())
            max_cap = IntMax(max_cap, tm.t.cap)
            if i >= window {
                old := CollKey(mut buf, i - window, klen)
                _ = tm.Remove(a4, old)
            }
        }
        cap4 := tm.t.cap
        tm.Free(a4)
        b4 := raw.Read(raw.Of(&c4))
        p4 := b4.peak
        l4 := b4.live
        try Print(w, "textmap cap {cap4} maxcap {max_cap} maxlen {max_len} peak {p4} after {l4}\n", (cap4, max_cap, max_len, p4, l4))
    }
}

// CollKey writes i as klen <= 16 decimal digits into buf and returns them
// as a str over buf (valid until buf is written again).
CollKey(buf: mut [16]u8, i: int, klen: int): str {
    var x := i
    var j := klen - 1
    for j >= 0 {
        buf[j] = Trunc[u8](48 + x % 10)
        x /= 10
        j -= 1
    }
    return StrOf(raw.Data(buf[..]), klen)
}
`

const collChurnModule = `import std.core

export Churn(w: mut Writer, a: &Alloc, n: int, window: int, klen: int): OutOfMemory|WriteError!void {
    try core.CollFixChurn(w, a, n, window, klen)
}
`

// TestStdColl_ChurnBounded: a Map over an Arena and a HeapMap, each over
// a counting heap, take n inserts each followed, window keys later, by its
// remove; then the cookbook Lru (capacity 100) takes n seeded accesses over
// 300 keys; then a TextMap takes the Map's churn with 8-digit keys, which
// it keeps in its slots, and with 16-digit keys, whose copies it allocates
// and frees (its heap peak may also hold the live keys' bytes); each of the
// 24 lines runs at both key lengths. The bounds: capacity stays below max(8, 32 L / 7) for a largest
// length L; at a window of 1 the capacity is 8 and every peak is under 64 KiB;
// a heap table's peak is its arrays plus the half-size arrays it grew from; an
// arena's peak budgets for doubled ordinary and aligned chunks plus their
// minimum initial chunks. Nothing is live after Free, and the
// peaks at n = 100,000 and n = 1,000,000 are equal.
func TestStdColl_ChurnBounded(t *testing.T) {
	t.Parallel()
	src := `import std.collchurn

export Main(sys: &System): OutOfMemory|WriteError!void {
    var out := sys.Stdout()
    a := sys.heap.Alloc()
    for klen in [8, 16] {
        for n in [100000, 1000000] {
            for window in [1, 50, 1000] {
                try collchurn.Churn(mut out, a, n, window, klen)
            }
        }
    }
}
`
	extra := map[string]string{"core/tests_collchurn.mk": collChurnCore, "collchurn/collchurn.mk": collChurnModule}
	got := collExec(t, collBuildWith(t, extra, src, "--release"))
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 48 {
		t.Fatalf("%d lines, want 48:\n%s", len(lines), got)
	}
	// A control array holds cap plus a mirrored group. Slots are 16 bytes
	// in Table[int, int] and 24 in Table[TextKey, int].
	arrays := func(c, slotBytes int) int {
		if c < 8 {
			return 0
		}
		return c*(1+slotBytes) + mGroupWidth
	}
	const firstChunk = 4096
	for i, l := range lines {
		window := []int{1, 50, 1000}[i/4%3]
		klen := []int{8, 16}[i/24]
		var kind string
		var capNow, maxCap, maxLen, peak, after, hits int
		switch i % 4 {
		case 0, 1, 3:
			if _, err := fmt.Sscanf(l, "%s cap %d maxcap %d maxlen %d peak %d after %d", &kind, &capNow, &maxCap, &maxLen, &peak, &after); err != nil {
				t.Fatalf("line %q: %v", l, err)
			}
			if maxLen != window+1 {
				t.Errorf("%q: largest length %d, want %d", l, maxLen, window+1)
			}
			if bound := max(8, 32*maxLen/7); maxCap >= bound && maxCap != 8 {
				t.Errorf("%q: capacity %d not below max(8, 32 L / 7) = %d", l, maxCap, bound)
			}
			if window == 1 && (maxCap != 8 || peak >= 64<<10) {
				t.Errorf("%q: window 1 needs capacity 8 and a peak under 64 KiB", l)
			}
			if bound := arrays(maxCap, 16) + arrays(maxCap/2, 16); kind == "heapmap" && peak > bound {
				t.Errorf("%q: heap peak above %d bytes for the current and previous arrays", l, bound)
			}
			// Controls are 16-aligned and the slots use ordinary chunks.
			// Each stream has a 4 KiB minimum. Its abandoned arrays total
			// less than the current ones, and its chunks double.
			if bound := 4*arrays(maxCap, 16) + 2*firstChunk; kind == "map" && peak > bound {
				t.Errorf("%q: arena peak above %d bytes for arrays and initial chunks", l, bound)
			}
			if bound := arrays(maxCap, 24) + arrays(maxCap/2, 24) + klen*maxLen; kind == "textmap" && peak > bound {
				t.Errorf("%q: heap peak above %d bytes for the current and previous arrays and live keys", l, bound)
			}
		case 2:
			if _, err := fmt.Sscanf(l, "lru hits %d peak %d after %d", &hits, &peak, &after); err != nil {
				t.Fatalf("line %q: %v", l, err)
			}
			if peak >= 64<<10 {
				t.Errorf("%q: Lru peak %d not under 64 KiB", l, peak)
			}
		}
		if after != 0 {
			t.Errorf("%q: %d bytes live after Free", l, after)
		}
		if i%24 >= 12 {
			small := strings.Fields(lines[i-12])
			large := strings.Fields(l)
			for k := range large {
				if (large[k] == "peak" || large[k] == "maxcap") && small[k+1] != large[k+1] {
					t.Errorf("%s grows with n: %q at 100,000, %q at 1,000,000", large[k], lines[i-12], l)
				}
			}
		}
	}
	t.Logf("\n%s", got)
}
