// Why: a growing container must consume its argument only on success; an
// OutOfMemory must leave the container and the caller's values intact.
// What: TestSTDCOLL_OutOfMemory, over std.coll's growing containers.
// How: a heap that fails exactly the n-th request, for every n until a run
// sees no failure (the CollFail pattern of std/std-coll_oom_test.go).

package std

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const c2OomFixture = `// Fixture of the std-coll2 OOM test: a heap that fails exactly one
// request, the fail_at-th alloc or growing resize counted from 0 (none when
// fail_at is negative), and drivers for every growing std.coll call.

import std.core
import std.raw
import std.rt
import std.coll

struct Fail {
    inner: *Alloc
    live: int
    reqs: int
    fail_at: int
    fails: int
}

const FailTable = core.AllocTable(alloc: FailAlloc, resize: FailResize, free: FailFree)

FailAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        var c := raw.Read(q)
        c.reqs += 1
        var p := raw.Null[u8]()
        if c.reqs - 1 == c.fail_at {
            c.fails += 1
        } else {
            p = core.AllocBytes(&*c.inner, size, align)
            if !raw.IsNull(p) {
                c.live += size
            }
        }
        raw.Write(q, c)
        return p
    }
}

FailResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
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
            ok = core.ResizeBytes(&*c.inner, p, old, new, align)
            if ok {
                c.live += new - old
            }
        }
        raw.Write(q, c)
        return ok
    }
}

FailFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        var c := raw.Read(q)
        core.FreeBytes(&*c.inner, p, size, align)
        c.live -= size
        raw.Write(q, c)
    }
}

Live(c: &Fail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).live
    }
}

Fails(c: &Fail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).fails
    }
}

linear struct Tok {
    id: int
}

TokId(t: Tok): int {
    Tok(id) := t
    return id
}

Fnv(h: u64, x: int): u64 {
    return (h ^ Trunc[u64](x)) *% 1099511628211
}

// Report prints one run's line: kind, fail_at, failures met, calls that
// failed, bad checks, tokens handed back, digest, live bytes at the end.
Report(w: mut Writer, kind: str, fail_at: int, c: &Fail, oom: int, bad: int, back: int, d: u64, live: int): WriteError!int {
    f := Fails(c)
    if bad > 0 {
        rt.WriteErr("coll2fix: a check failed\n".AsBytes())
    }
    try Print(w, "{kind} {fail_at} {f} {oom} {bad} {back} {d} {live}\n", (kind, fail_at, f, oom, bad, back, d, live))
    return f
}

// HeapDeque of 200 tokens pushed at alternating ends, then drained.
HeapDequeRun(w: mut Writer, a: &Alloc, fail_at: int): WriteError!int {
    var c := Fail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: fail_at, fails: 0)
    unsafe "c outlives the heap made over it, freed below" {
        h := core.MakeHeap(raw.PtrCast[Fail, u8](raw.Of(&c)), raw.Of(&FailTable))
        ha := h.Alloc()
        var oom := 0
        var bad := 0
        var back := 0
        var q := coll.HeapDeque[Tok].New(ha)
        for i in 0..200 {
            before := Live(&c)
            n := q.Len()
            t := Tok(id: i)
            if i % 3 == 0 {
                q.PushFront(ha, t) else {
                    oom += 1
                    back += TokId(t) + 1
                    if q.Len() != n || Live(&c) != before {
                        bad += 1
                    }
                    u := Tok(id: i)
                    q.PushFront(ha, u) else {
                        bad += 1
                        _ = TokId(u)
                    }
                }
            } else {
                q.Push(ha, t) else {
                    oom += 1
                    back += TokId(t) + 1
                    if q.Len() != n || Live(&c) != before {
                        bad += 1
                    }
                    u := Tok(id: i)
                    q.Push(ha, u) else {
                        bad += 1
                        _ = TokId(u)
                    }
                }
            }
        }
        var d: u64 = 14695981039346656037
        for x := q.PopFront() {
            d = Fnv(d, TokId(x))
        }
        q.FreeEmpty(ha)
        return try Report(w, "heapdeque", fail_at, &c, oom, bad, back, d, Live(&c))
    }
}

// HeapPut stores a token under a key new to t with Replace, since a
// B-tree's Set takes no linear value that is not Free: 0 when it is
// stored, the token's id plus 1 when OutOfMemory handed it back, and -1 when
// an old value came back (a failed check: every key is new).
HeapPut(t: mut coll.HeapBTree[int, Tok], a: &Alloc, k: int, x: Tok): int {
    o := t.Replace(a, k, x) else {
        return TokId(x) + 1
    }
    if v := o {
        _ = TokId(v)
        return -1
    }
    return 0
}

// Tik is Tok's plain twin for the arena containers, which hold no linear
// value.
struct Tik {
    id: int
}

// ArenaPut is HeapPut for the arena tree.
ArenaPut(t: mut coll.BTree[int, Tik], k: int, x: Tik): int {
    o := t.Replace(k, x) else {
        return x.id + 1
    }
    if v := o {
        _ = v.id
        return -1
    }
    return 0
}

// HeapBTree of 600 tokens under scattered keys, half removed, the rest
// drained by Remove in key order.
HeapBTreeRun(w: mut Writer, a: &Alloc, fail_at: int): WriteError!int {
    var c := Fail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: fail_at, fails: 0)
    unsafe "c outlives the heap made over it, freed below" {
        h := core.MakeHeap(raw.PtrCast[Fail, u8](raw.Of(&c)), raw.Of(&FailTable))
        ha := h.Alloc()
        var oom := 0
        var bad := 0
        var back := 0
        var t := coll.HeapBTree[int, Tok].New(ha)
        for i in 0..600 {
            k := i * 7919 % 1000
            before := Live(&c)
            n := t.Len()
            r := HeapPut(mut t, ha, k, Tok(id: i))
            if r < 0 {
                bad += 1
            } else if r > 0 {
                oom += 1
                back += r
                if t.Len() != n || t.Get(&k).IsSome() || Live(&c) != before {
                    bad += 1
                }
                if HeapPut(mut t, ha, k, Tok(id: i)) != 0 {
                    bad += 1
                }
            }
            if i % 4 == 3 {
                old := (i - 2) * 7919 % 1000
                if v := t.Remove(&old) {
                    _ = TokId(v)
                }
            }
        }
        var d: u64 = 14695981039346656037
        for k in 0..1000 {
            if v := t.Remove(&k) {
                d = Fnv(Fnv(d, k), TokId(v))
            }
        }
        if t.Len() != 0 {
            bad += 1
        }
        t.FreeEmpty(ha)
        return try Report(w, "heapbtree", fail_at, &c, oom, bad, back, d, Live(&c))
    }
}

MakeBits(ar: &Arena, n: int, i: int): OutOfMemory!void {
    var b := try coll.Bitset.New(ar, n)
    b.Set(i)
}

// Deque, PriorityQueue, BTree and Bitset in one arena over the failing
// heap: a failed arena page is OutOfMemory of the call that needed it.
ArenaRun(w: mut Writer, a: &Alloc, fail_at: int): WriteError!int {
    var c := Fail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: fail_at, fails: 0)
    unsafe "c outlives the heap made over it, freed below" {
        h := core.MakeHeap(raw.PtrCast[Fail, u8](raw.Of(&c)), raw.Of(&FailTable))
        var ar := Arena.New(&h) else {
            return try Report(w, "arena-new", fail_at, &c, 1, 0, 0, 0, Live(&c))
        }
        var oom := 0
        var bad := 0
        var back := 0
        var dq := coll.Deque[Tik].New(&ar)
        var pq := coll.PriorityQueue[int].New(&ar)
        var bt := coll.BTree[int, Tik].New(&ar)
        for i in 0..3000 {
            n := dq.Len()
            t := Tik(id: i)
            dq.Push(t) else {
                oom += 1
                back += t.id + 1
                if dq.Len() != n {
                    bad += 1
                }
                u := Tik(id: i)
                dq.Push(u) else {
                    bad += 1
                    _ = u.id
                }
            }
            pn := pq.Len()
            pq.Push(i * 7919 % 3001) else {
                oom += 1
                if pq.Len() != pn {
                    bad += 1
                }
                pq.Push(i * 7919 % 3001) else {
                    bad += 1
                }
            }
            k := i * 7919 % 5000
            bn := bt.Len()
            r := ArenaPut(mut bt, k, Tik(id: i))
            if r < 0 {
                bad += 1
            } else if r > 0 {
                oom += 1
                back += r
                if bt.Len() != bn || bt.Get(&k).IsSome() {
                    bad += 1
                }
                if ArenaPut(mut bt, k, Tik(id: i)) != 0 {
                    bad += 1
                }
            }
            if i % 500 == 499 {
                nb := 4096 * (i / 500 + 1)
                MakeBits(&ar, nb, i) else {
                    oom += 1
                    MakeBits(&ar, nb, i) else {
                        bad += 1
                    }
                }
            }
        }
        var d: u64 = 14695981039346656037
        for x := dq.PopFront() {
            d = Fnv(d, x.id)
        }
        for x := pq.Pop() {
            d = Fnv(d, x)
        }
        for k in 0..5000 {
            if v := bt.Remove(&k) {
                d = Fnv(Fnv(d, k), v.id)
            }
        }
        ar.Free()
        return try Report(w, "arena", fail_at, &c, oom, bad, back, d, Live(&c))
    }
}

export Run(w: mut Writer, a: &Alloc): WriteError!void {
    var n := -1
    for {
        f := try HeapDequeRun(w, a, n)
        if f == 0 && n >= 0 {
            break
        }
        n += 1
    }
    n = -1
    for {
        f := try HeapBTreeRun(w, a, n)
        if f == 0 && n >= 0 {
            break
        }
        n += 1
    }
    n = -1
    for {
        f := try ArenaRun(w, a, n)
        if f == 0 && n >= 0 {
            break
        }
        n += 1
    }
}
`

const c2OomProgram = `import std.coll2fix

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    try coll2fix.Run(mut out, sys.heap.Alloc())
}
`

var c2OomLine = regexp.MustCompile(`^(\S+) (-?\d+) (\d+) (\d+) (\d+) (\d+) (\d+) (-?\d+)$`)

// TestSTDCOLL_OutOfMemory: each growing container runs over a heap that
// fails exactly one request, the n-th, for every n from 0 until a run sees no
// failure. At each n the call that met the failure returns OutOfMemory with
// the container's length and the live bytes unchanged, a linear argument
// handed back to the caller, and the retried call succeeds; the final
// contents equal those of the run with no failure, and nothing is live after
// Free. Each output line is one run; the run with fail_at -1 is the reference
// for its kind.
//
// The failing heap needs std.core's MakeHeap, so the drivers live in a
// fixture module std.coll2fix of the test's std copy, listed in the copy's
// unsafe_modules. The fixture imports std.rt (for WriteErr), which also
// orders it after std.rt: without that, a dev build of it sometimes hits the
// lower ICE "facts of std.rt.Site asked before its module's impls are
// registered".
func TestSTDCOLL_OutOfMemory(t *testing.T) {
	t.Parallel()
	manifest, err := os.ReadFile("pkg.mk")
	if err != nil {
		t.Fatal(err)
	}
	m := string(manifest)
	if !strings.Contains(m, `unsafe_modules: [`) {
		t.Fatalf("pkg.mk: no unsafe_modules list to extend:\n%s", m)
	}
	m = strings.Replace(m, `unsafe_modules: [`, `unsafe_modules: ["coll2fix", `, 1)
	extra := map[string]string{"coll2fix/coll2fix.mk": c2OomFixture, "pkg.mk": m}
	for _, flags := range c2Flags {
		t.Run(c2FlagName(flags), func(t *testing.T) {
			t.Parallel()
			out := collExec(t, collBuildWith(t, extra, c2OomProgram, flags...))
			ref := map[string]string{}
			runs := map[string]int{}
			ooms := map[string]int{}
			for _, line := range strings.Split(strings.TrimSpace(strings.ReplaceAll(out, "\r\n", "\n")), "\n") {
				g := c2OomLine.FindStringSubmatch(line)
				if g == nil {
					t.Fatalf("bad line %q", line)
				}
				kind, failAt, fails, oom, bad, back, digest, live := g[1], g[2], g[3], g[4], g[5], g[6], g[7], g[8]
				if kind == "arena-new" {
					continue
				}
				runs[kind]++
				if bad != "0" || live != "0" {
					t.Errorf("%s: bad checks or live bytes after Free", line)
				}
				if failAt == "-1" {
					if fails != "0" || oom != "0" || back != "0" {
						t.Errorf("%s: the reference run failed", line)
					}
					ref[kind] = digest
					continue
				}
				if digest != ref[kind] {
					t.Errorf("%s: digest differs from the reference %s", line, ref[kind])
				}
				// A refused in-place resize is met and absorbed by a fresh
				// allocation (no call fails); a refused allocation fails
				// exactly one call.
				f, _ := strconv.Atoi(fails)
				o, _ := strconv.Atoi(oom)
				if o > f || o > 1 {
					t.Errorf("%s: %d failures met but %d calls failed", line, f, o)
				}
				if o > 0 {
					ooms[kind]++
				}
			}
			for _, kind := range []string{"heapdeque", "heapbtree", "arena"} {
				if runs[kind] < 3 || ooms[kind] < 2 {
					t.Errorf("%s: %d runs, %d with a failed call: the injection did not reach the containers", kind, runs[kind], ooms[kind])
				}
			}
			t.Logf("runs %v, runs with a failed call %v", runs, ooms)
		})
	}
}
