// Why: a container that runs out of memory must leave itself and the caller's
// values as they were, so the caller can retry.
// What: TestStdColl_OutOfMemoryEveryRequest over every growing container, plus
// NextCap, GrowSlots and zero-size elements.
// How: a heap fails exactly the n-th request, for every n until a run sees no
// failure; each run must end as the run with no failure and a Go model do.

package std

import (
	"fmt"
	"strings"
	"testing"
)

const collOomCore = `import std.raw

// A heap that fails exactly one request, the fail_at-th call of alloc or of
// a growing resize, counted from 0 (none when fail_at is negative), over
// another allocator; it counts the live bytes it handed out.
struct CollFail {
    inner: *Alloc
    live: int
    reqs: int
    fail_at: int
    fails: int
}

const CollFailTable = AllocTable(alloc: CollFailAlloc, resize: CollFailResize, free: CollFailFree)

CollFailAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is a live CollFail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, CollFail](ctx)
        var c := raw.Read(q)
        c.reqs += 1
        var p := raw.Null[u8]()
        if c.reqs - 1 == c.fail_at {
            c.fails += 1
        } else {
            p = AllocBytes(&*c.inner, size, align)
            if !raw.IsNull(p) {
                c.live += size
            }
        }
        raw.Write(q, c)
        return p
    }
}

CollFailResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is a live CollFail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, CollFail](ctx)
        var c := raw.Read(q)
        var ok := false
        var refused := false
        if new > old {
            c.reqs += 1
            if c.reqs - 1 == c.fail_at {
                c.fails += 1
                refused = true
            }
        }
        if !refused {
            ok = ResizeBytes(&*c.inner, p, old, new, align)
            if ok {
                c.live += new - old
            }
        }
        raw.Write(q, c)
        return ok
    }
}

CollFailFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is a live CollFail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, CollFail](ctx)
        var c := raw.Read(q)
        FreeBytes(&*c.inner, p, size, align)
        c.live -= size
        raw.Write(q, c)
    }
}

// The counter's fields, read through its address (the heap writes it
// through ctx).
CollLive(c: &CollFail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).live
    }
}

CollFails(c: &CollFail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).fails
    }
}

CollReqs(c: &CollFail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).reqs
    }
}

// A linear token: a value the caller must get back when an insert fails.
linear struct CollTok {
    id: int
}

CollTokId(t: CollTok): int {
    CollTok(id) := t
    return id
}

// Free gives a token up; it owns no memory.
impl Free for CollTok {
    Free(self, a: &Alloc) {
        _ = a
        _ = CollTokId(self)
    }
}

// CollTik is CollTok's plain twin for the arena containers, which hold no
// linear value.
struct CollTik {
    id: int
}

CollFnv(h: u64, x: int): u64 {
    return (h ^ Trunc[u64](x)) *% 1099511628211
}

CollEnsureHeap(m: mut HeapMap[int, CollTok], a: &Alloc, k: int, t: CollTok): OutOfMemory!void {
    _ = try m.Ensure(a, k, t)
}

CollEnsureArena(m: mut Map[int, CollTik], k: int, t: CollTik): OutOfMemory!void {
    old := try m.Replace(k, t)
    if o := old {
        _ = o.id
    }
}

CollSetArena(m: mut Map[int, CollTik], k: int, t: CollTik): OutOfMemory!void {
    old := try m.Replace(k, t)
    if o := old {
        _ = o.id
    }
}

CollEnsureText(m: mut TextMap[CollTok], a: &Alloc, k: str, t: CollTok): OutOfMemory!void {
    _ = try m.Ensure(a, k, t)
}

CollAdd(s: mut Set[int], k: int): OutOfMemory!void {
    _ = try s.Add(k)
}

CollHeapHas(m: &HeapMap[int, CollTok], k: int): bool {
    return TableFind(&m.t, HashOf(&k), &k) >= 0
}

// HeapList (Push, and Insert at 0 every third element) of 100 tokens, then
// HeapText (60 pushes of 7 bytes). Returns the injected failures.
CollOomHeapList(w: mut Writer, a: &Alloc, fail_at: int): WriteError!int {
    var c := CollFail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: fail_at, fails: 0)
    unsafe "c outlives the heap made over it, freed below" {
        h := MakeHeap(raw.PtrCast[CollFail, u8](raw.Of(&c)), raw.Of(&CollFailTable))
        ha := h.Alloc()
        var oom := 0
        var bad := 0
        var back := 0
        var plain := 0
        var l := HeapList[CollTok].New(ha)
        for i in 0..100 {
            before := CollLive(&c)
            n := l.Len()
            t := CollTok(id: i)
            if i % 3 == 0 {
                l.Insert(ha, 0, t) else {
                    oom += 1
                    back += CollTokId(t) + 1
                    if l.Len() != n || CollLive(&c) != before {
                        bad += 1
                    }
                    u := CollTok(id: i)
                    l.Insert(ha, 0, u) else {
                        bad += 1
                        _ = CollTokId(u)
                    }
                }
            } else {
                l.Push(ha, t) else {
                    oom += 1
                    back += CollTokId(t) + 1
                    if l.Len() != n || CollLive(&c) != before {
                        bad += 1
                    }
                    u := CollTok(id: i)
                    l.Push(ha, u) else {
                        bad += 1
                        _ = CollTokId(u)
                    }
                }
            }
        }
        var d: u64 = 14695981039346656037
        for i in 0..l.Len() {
            if x := l.Get(i) {
                d = CollFnv(CollFnv(d, i), x.id)
            }
        }
        ln := l.Len()
        for l.Len() > 0 {
            if t := l.Pop() {
                _ = CollTokId(t)
            }
        }
        l.Free(ha)
        var s := HeapText.New(ha)
        for _ in 0..60 {
            before := CollLive(&c)
            n := s.Str().Len()
            s.Push(ha, "abcdefg") else {
                oom += 1
                plain += 1
                if s.Str().Len() != n || CollLive(&c) != before {
                    bad += 1
                }
                s.Push(ha, "abcdefg") else {
                    bad += 1
                }
            }
        }
        tl := s.Str().Len()
        s.Free(ha)
        after := CollLive(&c)
        f := CollFails(&c)
        r := CollReqs(&c)
        try Print(w, "heaplist {fail_at} fails {f} reqs {r} oom {oom} bad {bad} back {back} plain {plain} len {ln} digest {d} text {tl} after {after}\n", (fail_at, f, r, oom, bad, back, plain, ln, d, tl, after))
        return f
    }
}

// HeapMap (Set on even i, Ensure on odd) of 300 tokens, a third of them
// removed as it goes, then TextMap.Ensure over keys (the empty key
// included), a fifth removed. Returns the injected failures.
CollOomHeapMap(w: mut Writer, a: &Alloc, fail_at: int, keys: []str): WriteError!int {
    var c := CollFail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: fail_at, fails: 0)
    unsafe "c outlives the heap made over it, freed below" {
        h := MakeHeap(raw.PtrCast[CollFail, u8](raw.Of(&c)), raw.Of(&CollFailTable))
        ha := h.Alloc()
        var oom := 0
        var bad := 0
        var back := 0
        var plain := 0
        var m := HeapMap[int, CollTok].New(ha)
        for i in 0..300 {
            k := i * 7919 % 1000
            before := CollLive(&c)
            n := m.Len()
            t := CollTok(id: i)
            if i % 2 == 0 {
                m.Set(ha, k, t) else {
                    oom += 1
                    back += CollTokId(t) + 1
                    if m.Len() != n || CollHeapHas(&m, k) || CollLive(&c) != before {
                        bad += 1
                    }
                    u := CollTok(id: i)
                    m.Set(ha, k, u) else {
                        bad += 1
                        _ = CollTokId(u)
                    }
                }
            } else {
                CollEnsureHeap(mut m, ha, k, t) else {
                    oom += 1
                    back += CollTokId(t) + 1
                    if m.Len() != n || CollHeapHas(&m, k) || CollLive(&c) != before {
                        bad += 1
                    }
                    u := CollTok(id: i)
                    CollEnsureHeap(mut m, ha, k, u) else {
                        bad += 1
                        _ = CollTokId(u)
                    }
                }
            }
            if i % 3 == 2 {
                old := (i - 1) * 7919 % 1000
                if v := m.Remove(&old) {
                    _ = CollTokId(v)
                }
            }
        }
        var d: u64 = 0
        for i in 0..300 {
            k := i * 7919 % 1000
            if v := m.Get(&k) {
                d +%= Trunc[u64](k * 1000003 + v.id)
            }
        }
        ln := m.Len()
        for i in 0..300 {
            k := i * 7919 % 1000
            if v := m.Remove(&k) {
                _ = CollTokId(v)
            }
        }
        m.Free(ha)
        var tm := TextMap[CollTok].New(ha)
        for i in 0..keys.Len() {
            k := keys[i]
            before := CollLive(&c)
            n := tm.Len()
            t := CollTok(id: i)
            CollEnsureText(mut tm, ha, k, t) else {
                oom += 1
                back += CollTokId(t) + 1
                if tm.Len() != n || tm.Get(k).IsSome() || CollLive(&c) != before {
                    bad += 1
                }
                u := CollTok(id: i)
                CollEnsureText(mut tm, ha, k, u) else {
                    bad += 1
                    _ = CollTokId(u)
                }
            }
            if i % 5 == 4 {
                if v := tm.Remove(ha, keys[i - 2]) {
                    _ = CollTokId(v)
                }
            }
        }
        var e: u64 = 0
        var it := tm.Iter()
        for x := it.Next() {
            e +%= Trunc[u64](x.key.Len() * 1000003 + x.value.id)
        }
        tn := tm.Len()
        for i in 0..keys.Len() {
            if v := tm.Remove(ha, keys[i]) {
                _ = CollTokId(v)
            }
        }
        tm.Free(ha)
        after := CollLive(&c)
        f := CollFails(&c)
        r := CollReqs(&c)
        try Print(w, "heapmap {fail_at} fails {f} reqs {r} oom {oom} bad {bad} back {back} plain {plain} len {ln} digest {d} text {tn} {e} after {after}\n", (fail_at, f, r, oom, bad, back, plain, ln, d, tn, e, after))
        return f
    }
}

// On one Arena: Map (Replace on every i: an arena Map's Set and Ensure of
// a linear value are E0701) of 300 tokens with a
// third removed, Set.Add of the same keys, List.Push of 300 tokens.
// Returns the injected failures.
CollOomArena(w: mut Writer, a: &Alloc, fail_at: int): WriteError!int {
    var c := CollFail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: fail_at, fails: 0)
    unsafe "c outlives the heap made over it, freed below" {
        h := MakeHeap(raw.PtrCast[CollFail, u8](raw.Of(&c)), raw.Of(&CollFailTable))
        var ar := Arena.New(&h) else {
            after := CollLive(&c)
            try Print(w, "arena {fail_at} new failed after {after}\n", (fail_at, after))
            return CollFails(&c)
        }
        var oom := 0
        var bad := 0
        var back := 0
        var plain := 0
        var m := Map[int, CollTik].New(&ar)
        var st := Set[int].New(&ar)
        var l := List[CollTik].New(&ar)
        for i in 0..300 {
            k := i * 7919 % 1000
            before := CollLive(&c)
            n := m.Len()
            t := CollTik(id: i)
            if i % 2 == 0 {
                CollSetArena(mut m, k, t) else {
                    oom += 1
                    back += t.id + 1
                    if m.Len() != n || m.Has(&k) || CollLive(&c) != before {
                        bad += 1
                    }
                    u := CollTik(id: i)
                    CollSetArena(mut m, k, u) else {
                        bad += 1
                        _ = u.id
                    }
                }
            } else {
                CollEnsureArena(mut m, k, t) else {
                    oom += 1
                    back += t.id + 1
                    if m.Len() != n || m.Has(&k) || CollLive(&c) != before {
                        bad += 1
                    }
                    u := CollTik(id: i)
                    CollEnsureArena(mut m, k, u) else {
                        bad += 1
                        _ = u.id
                    }
                }
            }
            if i % 3 == 2 {
                old := (i - 1) * 7919 % 1000
                if v := m.Remove(&old) {
                    _ = v.id
                }
            }
            sn := st.t.Count()
            sb := CollLive(&c)
            if !st.Has(&k) {
                CollAdd(mut st, k) else {
                    oom += 1
                    plain += 1
                    if st.t.Count() != sn || st.Has(&k) || CollLive(&c) != sb {
                        bad += 1
                    }
                    CollAdd(mut st, k) else {
                        bad += 1
                    }
                }
            }
            lb := CollLive(&c)
            ln := l.Len()
            lt := CollTik(id: i)
            l.Push(lt) else {
                oom += 1
                back += lt.id + 1
                if l.Len() != ln || CollLive(&c) != lb {
                    bad += 1
                }
                lu := CollTik(id: i)
                l.Push(lu) else {
                    bad += 1
                    _ = lu.id
                }
            }
        }
        var d: u64 = 0
        for i in 0..300 {
            k := i * 7919 % 1000
            if v := m.Get(&k) {
                d +%= Trunc[u64](k * 1000003 + v.id)
            }
            if st.Has(&k) {
                d +%= 7
            }
        }
        var ld: u64 = 14695981039346656037
        for i in 0..l.Len() {
            if x := l.Get(i) {
                ld = CollFnv(CollFnv(ld, i), x.id)
            }
        }
        mn := m.Len()
        sn := st.t.Count()
        for i in 0..300 {
            k := i * 7919 % 1000
            if v := m.Remove(&k) {
                _ = v.id
            }
        }
        for l.Len() > 0 {
            if t := l.Pop() {
                _ = t.id
            }
        }
        ar.Free()
        after := CollLive(&c)
        f := CollFails(&c)
        r := CollReqs(&c)
        try Print(w, "arena {fail_at} fails {f} reqs {r} oom {oom} bad {bad} back {back} plain {plain} len {mn} {sn} digest {d} {ld} after {after}\n", (fail_at, f, r, oom, bad, back, plain, mn, sn, d, ld, after))
        return f
    }
}

// 4.6's growth arithmetic at the edges of int, and zero-size elements.
CollGrowEdges(w: mut Writer, a: &Alloc): WriteError!void {
    half := GrowLimit / 2
    c1 := NextCap(0, 1)
    c2 := NextCap(4, 5)
    c3 := NextCap(8, 100)
    c4 := NextCap(half, half + 1)
    c5 := NextCap(half + 1, half + 2)
    c6 := NextCap(GrowLimit, GrowLimit)
    try Print(w, "nextcap {c1} {c2} {c3} {c4} {c5} {c6}\n", (c1, c2, c3, c4, c5, c6))
    var c := CollFail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: -1, fails: 0)
    unsafe "c outlives the heap made over it" {
        h := MakeHeap(raw.PtrCast[CollFail, u8](raw.Of(&c)), raw.Of(&CollFailTable))
        ha := h.Alloc()
        // c * 8 overflows int: out of memory before any request.
        p1 := GrowSlots[int](ha, Dangling[int](), 0, 0, GrowLimit / 8 + 1, true)
        r1 := CollReqs(&c)
        // 2^61 bytes: a request the heap refuses.
        p2 := GrowSlots[int](ha, Dangling[int](), 0, 0, 0x100000000000000, true)
        r2 := CollReqs(&c)
        n1 := raw.IsNull(p1)
        n2 := raw.IsNull(p2)
        var l := HeapList[[0]u8].New(ha)
        for _ in 0..1000 {
            l.Push(ha, []) else {
                l.Free(ha)
                return
            }
        }
        zl := l.Len()
        l.Free(ha)
        var hl := HeapList[int].New(ha)
        hl.Push(ha, 1) else {
            hl.Free(ha)
            return
        }
        hl.Free(ha)
        r3 := CollReqs(&c)
        live := CollLive(&c)
        try Print(w, "growslots {n1} {r1} {n2} {r2} zero {zl} reqs {r3} after {live}\n", (n1, r1, n2, r2, zl, r3, live))
    }
}

export CollOomAll(w: mut Writer, a: &Alloc, keys: []str): WriteError!void {
    var n := -1
    for {
        f := try CollOomHeapList(w, a, n)
        if n >= 0 && f == 0 {
            break
        }
        n += 1
    }
    n = -1
    for {
        f := try CollOomHeapMap(w, a, n, keys)
        if n >= 0 && f == 0 {
            break
        }
        n += 1
    }
    n = -1
    for {
        f := try CollOomArena(w, a, n)
        if n >= 0 && f == 0 {
            break
        }
        n += 1
    }
    try CollGrowEdges(w, a)
}
`

const collOomModule = `import std.core

export Run(w: mut Writer, a: &Alloc, keys: []str): WriteError!void {
    try core.CollOomAll(w, a, keys)
}
`

// collOomKeys are TextMap's keys: the empty key, then distinct keys of 1 to
// 40 bytes.
func collOomKeys() []string {
	keys := []string{""}
	for i := 1; len(keys) < 120; i++ {
		keys = append(keys, fmt.Sprintf("k%d-%s", i, strings.Repeat("x", i%37)))
	}
	return keys
}

// collOomModel is the Go model of the three scenarios with no failure: the
// digests the program prints.
func collOomModel(keys []string) (listDigest uint64, mapLen int, mapDigest uint64, textLen int, textDigest uint64, arenaDigest uint64, setLen int, arenaList uint64) {
	fnv := func(h uint64, x int) uint64 { return (h ^ uint64(x)) * 1099511628211 }
	var l []int
	for i := 0; i < 100; i++ {
		if i%3 == 0 {
			l = append([]int{i}, l...)
		} else {
			l = append(l, i)
		}
	}
	listDigest = 14695981039346656037
	for i, x := range l {
		listDigest = fnv(fnv(listDigest, i), x)
	}
	m := map[int]int{}
	for i := 0; i < 300; i++ {
		k := i * 7919 % 1000
		m[k] = i
		if i%3 == 2 {
			delete(m, (i-1)*7919%1000)
		}
	}
	for k, v := range m {
		mapDigest += uint64(k*1000003 + v)
	}
	mapLen = len(m)
	tm := map[string]int{}
	for i, k := range keys {
		if _, ok := tm[k]; !ok {
			tm[k] = i
		}
		if i%5 == 4 {
			delete(tm, keys[i-2])
		}
	}
	for k, v := range tm {
		textDigest += uint64(len(k)*1000003 + v)
	}
	textLen = len(tm)
	arenaDigest = mapDigest + 7*300
	setLen = 300
	arenaList = 14695981039346656037
	for i := 0; i < 300; i++ {
		arenaList = fnv(fnv(arenaList, i), i)
	}
	return
}

// TestStdColl_OutOfMemoryEveryRequest: every growing container runs over a
// heap that fails exactly one request, the n-th, for every n from 0 until a
// run sees no failure. An operation consumes its arguments on success only: at
// each n the operation that met the failure returns OutOfMemory with the
// container and the live bytes unchanged and its linear argument still owned
// by the caller (it is handed back in the else block), the retried operation
// succeeds, and the final contents equal those of the run with no failure,
// which equal a Go model. Nothing is live after Free. Also the growth
// arithmetic at the edges of int (NextCap, GrowSlots) and zero-size elements,
// which allocate nothing.
func TestStdColl_OutOfMemoryEveryRequest(t *testing.T) {
	t.Parallel()
	keys := collOomKeys()
	var lit strings.Builder
	lit.WriteString("[")
	for i, k := range keys {
		if i > 0 {
			lit.WriteString(", ")
		}
		fmt.Fprintf(&lit, "%q", k)
	}
	lit.WriteString("]")
	src := `import std.colloom

const Keys = ` + lit.String() + `

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    a := sys.heap.Alloc()
    try colloom.Run(mut out, a, Keys[..])
}
`
	extra := map[string]string{"core/tests_colloom.mk": collOomCore, "colloom/colloom.mk": collOomModule}
	for _, flags := range [][]string{nil, {"--release"}} {
		got := collExec(t, collBuildWith(t, extra, src, flags...))
		checkCollOom(t, flags, keys, got)
	}
}

func checkCollOom(t *testing.T, flags []string, keys []string, got string) {
	t.Helper()
	ld, mlen, md, tlen, td, ad, slen, al := collOomModel(keys)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	runs := map[string]int{}
	ooms := map[string]int{}
	tokens := map[string]int{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		// field i's integer, after the name at i - 1.
		num := func(name string, skip int) string {
			for i := 0; i+1+skip < len(f); i++ {
				if f[i] == name {
					return f[i+1+skip]
				}
			}
			t.Fatalf("%v: %q has no %s", flags, l, name)
			return ""
		}
		switch f[0] {
		case "heaplist", "heapmap", "arena":
			if f[2] == "new" { // the arena's first chunk was the failed request
				if num("after", 0) != "0" {
					t.Errorf("%v: %q: bytes live", flags, l)
				}
				runs[f[0]]++
				continue
			}
			runs[f[0]]++
			fails, oom, back := num("fails", 0), num("oom", 0), num("back", 0)
			if num("bad", 0) != "0" {
				t.Errorf("%v: %q: a failed operation changed the container or the live bytes, or a retry failed", flags, l)
			}
			if num("after", 0) != "0" {
				t.Errorf("%v: %q: bytes live after Free", flags, l)
			}
			if oom != "0" && oom != "1" {
				t.Errorf("%v: %q: one injected failure gave %s failed operations", flags, l, oom)
			}
			if oom == "1" {
				ooms[f[0]]++
				if fails != "1" {
					t.Errorf("%v: %q: an operation failed with no injected failure", flags, l)
				}
			}
			// A failed insert of a token hands it back (back is its id + 1);
			// a failed HeapText.Push or Set.Add of an int counts in plain.
			plain := num("plain", 0)
			if oom == "1" && (back == "0") != (plain == "1") {
				t.Errorf("%v: %q: a failed token insert must hand its token back, and only then", flags, l)
			}
			if oom == "1" {
				kind := " token"
				if plain == "1" {
					kind = " plain"
				}
				tokens[f[0]+kind]++
			}
			want := map[string]string{
				"heaplist": fmt.Sprintf("len 100 digest %d text 420", ld),
				"heapmap":  fmt.Sprintf("len %d digest %d text %d %d", mlen, md, tlen, td),
				"arena":    fmt.Sprintf("len %d %d digest %d %d", mlen, slen, ad, al),
			}[f[0]]
			if !strings.Contains(l, want) {
				t.Errorf("%v: %q: contents differ from the Go model %q", flags, l, want)
			}
		case "nextcap":
			want := fmt.Sprintf("nextcap 4 8 100 %d %d %d", (1<<63-1)/2*2, (1<<63-1)/2+2, 1<<63-1)
			if l != want {
				t.Errorf("%v: %q, want %q", flags, l, want)
			}
		case "growslots":
			if want := "growslots true 0 true 1 zero 1000 reqs 2 after 0"; l != want {
				t.Errorf("%v: %q, want %q", flags, l, want)
			}
		default:
			t.Errorf("%v: unexpected line %q", flags, l)
		}
	}
	for _, k := range []string{"heaplist", "heapmap", "arena"} {
		if runs[k] < 3 || ooms[k] == 0 {
			t.Errorf("%v: %s: %d runs, %d with a failed operation: the injection reached nothing", flags, k, runs[k], ooms[k])
		}
	}
	// The arena takes 5 requests in all (its chunks are large), and both
	// failed operations there are token inserts, so no "arena plain" kind.
	for _, k := range []string{"heaplist token", "heaplist plain", "heapmap token", "arena token"} {
		if tokens[k] == 0 {
			t.Errorf("%v: no run failed a %s operation", flags, k)
		}
	}
	t.Logf("%v: runs %v, runs with a failed operation %v, by kind %v", flags, runs, ooms, tokens)
}
