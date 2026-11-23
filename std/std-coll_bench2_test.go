// Why: std's containers and sorts need timings next to Go's for the same work.
// What: TestStdColl_BenchContainers: List and HeapList Push, Map iteration,
// HeapMap and TextMap, SortStable and Search, each against Go.
// How: a --release program times itself through IoTable.now, beside Go on the
// same machine. Skips unless MK_BENCH=1.

package std

import (
	"cmp"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

const bench2Program = `import std.collfix

Less(a: &int, b: &int): bool {
    return *a < *b
}

// Ns is t per op in ns, at least 1 op.
Ns(t: i64, ops: int): i64 {
    return t / Trunc[i64](IntMaxB(ops, 1))
}

IntMaxB(a: int, b: int): int {
    if a > b {
        return a
    }
    return b
}

Lists(sys: &System, out: mut Writer, n: int): WriteError|OutOfMemory!void {
    reps := 20000000 / n
    var sum := 0
    t0 := collfix.Now(sys.io)
    for _ in 0..reps {
        var a := try sys.arena.Sub()
        var l := List[int].New(&a)
        for i in 0..n {
            l.Push(i) else {
                a.Free()
                fail OutOfMemory
            }
        }
        sum += l.Len()
        a.Free()
    }
    t1 := collfix.Now(sys.io)
    h := sys.heap.Alloc()
    for _ in 0..reps {
        var l := HeapList[int].New(h)
        for i in 0..n {
            l.Push(h, i) else {
                l.Free(h)
                fail OutOfMemory
            }
        }
        sum += l.Len()
        l.Free(h)
    }
    t2 := collfix.Now(sys.io)
    lp := (t1 - t0) * 1000 / Trunc[i64](reps * n)
    hp := (t2 - t1) * 1000 / Trunc[i64](reps * n)
    try Print(out, "push n {n} list_ps {lp} heaplist_ps {hp} {sum}\n", (n, lp, hp, sum))
}

Maps(sys: &System, out: mut Writer, n: int): WriteError|OutOfMemory!void {
    var a := try sys.arena.Sub()
    defer a.Free()
    var m := Map[int, int].New(&a)
    var s: u64 = 1
    for i in 0..n {
        s = s *% 6364136223846793005 +% 1442695040888963407
        try m.Set(Trunc[int](s >> 1), i)
    }
    reps := 20000000 / n
    var sum := 0
    t0 := collfix.Now(sys.io)
    for _ in 0..reps {
        var it := m.Iter()
        for e := it.Next() {
            sum +%= *e.value
        }
    }
    t1 := collfix.Now(sys.io)
    h := sys.heap.Alloc()
    var hm := HeapMap[int, int].New(h)
    s = 1
    for i in 0..n {
        s = s *% 6364136223846793005 +% 1442695040888963407
        hm.Set(h, Trunc[int](s >> 1), i) else {
            hm.Free(h)
            fail OutOfMemory
        }
    }
    t2 := collfix.Now(sys.io)
    for _ in 0..4 {
        s = 1
        for _ in 0..n {
            s = s *% 6364136223846793005 +% 1442695040888963407
            k := Trunc[int](s >> 1)
            if v := hm.Get(&k) {
                sum +%= *v
            }
        }
    }
    t3 := collfix.Now(sys.io)
    hm.Free(h)
    var kl := List[str].New(&a)
    for i in 0..n {
        try kl.Push(try a.Fmt("key{i}", (i)))
    }
    keys := kl.Done()
    t4 := collfix.Now(sys.io)
    var tm := TextMap[int].New(h)
    for i in 0..n {
        _ = tm.Ensure(h, keys[i], i) else {
            tm.Free(h)
            fail OutOfMemory
        }
    }
    t5 := collfix.Now(sys.io)
    for _ in 0..4 {
        for i in 0..n {
            if v := tm.Get(keys[i]) {
                sum +%= *v
            }
        }
    }
    t6 := collfix.Now(sys.io)
    tm.Free(h)
    iter := (t1 - t0) * 1000 / Trunc[i64](reps * n)
    hs := Ns(t2 - t1, n)
    hg := Ns(t3 - t2, 4 * n)
    ts := Ns(t5 - t4, n)
    tg := Ns(t6 - t5, 4 * n)
    try Print(out, "maps n {n} iter_ps {iter} heapmap set_ns {hs} get_ns {hg} textmap ensure_ns {ts} get_ns {tg} {sum}\n", (n, iter, hs, hg, ts, tg, sum))
}

Stable(sys: &System, out: mut Writer, n: int, distinct: int): WriteError|OutOfMemory!void {
    var a := try sys.arena.Sub()
    defer a.Free()
    var xs := try a.Slice[int](n)
    var best: i64 = 9223372036854775807
    for _ in 0..3 {
        var s: u64 = 1
        for i in 0..n {
            s = s *% 6364136223846793005 +% 1442695040888963407
            xs[i] = Trunc[int](s >> 1) % distinct
        }
        t0 := collfix.Now(sys.io)
        try xs.SortStable(sys.heap.Alloc(), Less)
        t1 := collfix.Now(sys.io)
        if t1 - t0 < best {
            best = t1 - t0
        }
    }
    var sum := 0
    t2 := collfix.Now(sys.io)
    var s: u64 = 9
    for _ in 0..1000000 {
        s = s *% 6364136223846793005 +% 1442695040888963407
        q := Trunc[int](s >> 1) % distinct
        sum +%= xs.Search(&q, Less)
    }
    t3 := collfix.Now(sys.io)
    search := Ns(t3 - t2, 1000000)
    try Print(out, "stable n {n} distinct {distinct} ns {best} search_ns {search} {sum}\n", (n, distinct, best, search, sum))
}

export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    for n in [100, 10000, 1000000] {
        try Lists(sys, mut out, n)
    }
    for n in [1000, 65536] {
        try Maps(sys, mut out, n)
    }
    try Stable(sys, mut out, 1000000, 1000000000)
    try Stable(sys, mut out, 1000000, 100)
}
`

// goBench2 does bench2Program's work in Go and returns the same lines.
func goBench2() string {
	var b strings.Builder
	// Repeat f until 50 ms have passed (Windows' time.Now is coarse) and
	// return ns per op for ops per call.
	rate := func(ops int, f func()) float64 {
		t0 := time.Now()
		calls := 0
		for time.Since(t0) < 50*time.Millisecond || calls < 3 {
			f()
			calls++
		}
		return float64(time.Since(t0).Nanoseconds()) / float64(calls*ops)
	}
	sum := 0
	for _, n := range []int{100, 10000, 1000000} {
		push := rate(n, func() {
			var l []int
			for i := 0; i < n; i++ {
				l = append(l, i)
			}
			sum += len(l)
		})
		fmt.Fprintf(&b, "push n %d append_ps %.0f\n", n, push*1000)
	}
	for _, n := range []int{1000, 65536} {
		m := map[int]int{}
		var s uint64 = 1
		for i := 0; i < n; i++ {
			s = s*6364136223846793005 + 1442695040888963407
			m[int(s>>1)] = i
		}
		iter := rate(n, func() {
			for _, v := range m {
				sum += v
			}
		})
		set := rate(n, func() {
			mm := map[int]int{}
			var s uint64 = 1
			for i := 0; i < n; i++ {
				s = s*6364136223846793005 + 1442695040888963407
				mm[int(s>>1)] = i
			}
		})
		get := rate(n, func() {
			var s uint64 = 1
			for i := 0; i < n; i++ {
				s = s*6364136223846793005 + 1442695040888963407
				sum += m[int(s>>1)]
			}
		})
		keys := make([]string, n)
		for i := range keys {
			keys[i] = fmt.Sprintf("key%d", i)
		}
		tm := map[string]int{}
		tset := rate(n, func() {
			tm = map[string]int{}
			for i, k := range keys {
				if _, ok := tm[k]; !ok {
					tm[k] = i
				}
			}
		})
		tget := rate(n, func() {
			for _, k := range keys {
				sum += tm[k]
			}
		})
		fmt.Fprintf(&b, "maps n %d iter_ps %.0f map set_ns %.1f get_ns %.1f map[string] ensure_ns %.1f get_ns %.1f\n", n, iter*1000, set, get, tset, tget)
	}
	for _, distinct := range []int{1000000000, 100} {
		n := 1000000
		xs := make([]int, n)
		best := time.Duration(1 << 62)
		for r := 0; r < 3; r++ {
			var s uint64 = 1
			for i := range xs {
				s = s*6364136223846793005 + 1442695040888963407
				xs[i] = int(s>>1) % distinct
			}
			t0 := time.Now()
			slices.SortStableFunc(xs, cmp.Compare[int])
			best = min(best, time.Since(t0))
		}
		var s uint64 = 9
		t0 := time.Now()
		for i := 0; i < 1000000; i++ {
			s = s*6364136223846793005 + 1442695040888963407
			q := int(s>>1) % distinct
			sum += sort.Search(n, func(j int) bool { return xs[j] >= q })
		}
		search := float64(time.Since(t0).Nanoseconds()) / 1e6
		fmt.Fprintf(&b, "stable n %d distinct %d ns %d (slices.SortStableFunc) search_ns %.1f (sort.Search) %d\n", n, distinct, best.Nanoseconds(), search, sum)
	}
	return b.String()
}

// TestStdColl_BenchContainers logs std's numbers and Go's for the same work.
func TestStdColl_BenchContainers(t *testing.T) {
	if os.Getenv("MK_BENCH") != "1" {
		t.Skip("set MK_BENCH=1 to run the std-coll benchmarks")
	}
	var extra map[string]string
	mk := collExec(t, collBuildWith(t, extra, bench2Program, "--release"))
	t.Logf("machine: %s/%s, %d CPUs, %s", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version())
	t.Logf("mk build --release:\n%s", mk)
	t.Logf("Go:\n%s", goBench2())
}
