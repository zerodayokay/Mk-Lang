// Why: SortStable's stack scratch, and SortStableInPlace's stack cache, hold
// values of any T in a [512]u64 local, read and written through a raw
// pointer cast, sound only without TBAA metadata.
// What: TestSortCacheNoTBAA and TestSortCacheSizesMatchModel, with the extra
// element types sortCacheSizesExtra and the cases sortCacheCase.
// How: reads the IR of a sort through both for !tbaa, and sorts each cache
// size against slices.SortStableFunc and the model's count of less calls.

package std

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sortCacheTBAAProgram = `struct E {
    k: int
    tag: u8
    name: str
    w: u32
}

ByK(a: &E, b: &E): bool {
    return a.k < b.k
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var l := List[E].New(sys.arena)
    var l2 := List[E].New(sys.arena)
    var s: u64 = 5
    for i in 0..2000 {
        s = s *% 6364136223846793005 +% 1442695040888963407
        e := E(k: Trunc[int](s >> 33) % 97, tag: Trunc[u8](i), name: "x", w: Trunc[u32](i * 7))
        try l.Push(e)
        try l2.Push(e)
    }
    try l.SortStable(sys.heap.Alloc(), ByK)
    l2.SortStableInPlace(ByK)
    var ok := true
    for i in 1..l.Len() {
        ok = ok && l[i - 1].k <= l[i].k && l2[i - 1].k <= l2[i].k
    }
    var out := sys.Stdout()
    try Print(mut out, "{ok}\n", (ok))
}
`

// TestSortCacheNoTBAA pins what SortStable's stack scratch (mem_drift.mk,
// DriftStackCap) and SortStableInPlace's stack cache (core_sort_stable.mk,
// BlkCacheCap) rest on: each is a [512]u64 local that holds values of any T
// aligned to at most 8, written and read through a raw pointer cast, which
// is sound only while the compiler emits no type-based alias metadata; with
// !tbaa on its loads and stores LLVM could take a store of a T and a later
// load of a u64 field to the same bytes as unordered. The test builds a
// program whose SortStable and SortStableInPlace move structs of mixed fields
// (an int, a u8, a str and a u32), with --emit-ll, in dev and release, and
// fails on any tbaa metadata in the IR codegen wrote; it also checks the IR
// holds DriftSort and the cache's merge, so it read the code that needs the
// rule.
func TestSortCacheNoTBAA(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{nil, {"--release"}} {
		root := t.TempDir()
		stdDir := copyStd(t, root, nil)
		if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(sortCacheTBAAProgram), 0o644); err != nil {
			t.Fatal(err)
		}
		ll := filepath.Join(root, "ll")
		args := append([]string{"build"}, flags...)
		args = append(args, "--emit-ll", ll, "-o", filepath.Join(root, "main.exe"), "main.mk")
		out, status := mkCmd(t, root, stdDir, args...)
		if status != 0 {
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			t.Fatalf("mk build %v: exit %d\n%s", flags, status, out)
		}
		files, cacheMerge, drift := 0, false, false
		err := filepath.WalkDir(ll, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ll") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files++
			text := string(b)
			if i := strings.Index(text, "tbaa"); i >= 0 {
				line := text[strings.LastIndex(text[:i], "\n")+1:]
				line, _, _ = strings.Cut(line, "\n")
				t.Errorf("%v: %s holds type-based alias metadata, which SortStable's stack scratch and cache cannot have: %s", flags, filepath.Base(p), line)
			}
			cacheMerge = cacheMerge || strings.Contains(text, "BlkMergeExternal")
			drift = drift || strings.Contains(text, "DriftSort")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if files == 0 || !cacheMerge || !drift {
			t.Fatalf("%v: %d IR files, cache merge seen %v, DriftSort seen %v: the test no longer reads the code", flags, files, cacheMerge, drift)
		}
	}
}

// sortCacheSizesExtra adds, to blockMergeHead (std-coll_blockmerge_test.go),
// element types that get each kind of stack cache: 8 bytes (512 slots), 40
// (102), 16 aligned to 16 (none), 4,096 (1) and 4,104 (none); RunW sorts the
// pattern's keys as type w stably with SortStableInPlace, counting the calls
// of less, and prints the digest of the ids in order and the count.
const sortCacheSizesExtra = `
struct W8 {
    k: i32
    id: i32
}

struct W40 {
    k: int
    id: int
    a: int
    b: int
    c: int
}

align(16) struct WA16 {
    k: int
    id: int
}

struct W4096 {
    k: int
    id: int
    pad: [510]u64
}

struct WBig {
    k: int
    id: int
    pad: [511]u64
}

RunW(out: mut Writer, a: &Alloc, w: int, pat: int, n: int): OutOfMemory|WriteError!void {
    var keys := HeapList[P].New(a)
    defer keys.Free(a)
    for _ in 0..n {
        try keys.Push(a, P(k: 0, id: 0))
    }
    Keys(pat, keys.ItemsMut())
    c := Cell[int].New(0)
    var h: u64 = 14695981039346656037
    if w == 8 {
        var l := HeapList[W8].New(a)
        defer l.Free(a)
        for i in 0..n {
            try l.Push(a, W8(k: Trunc[i32](keys[i].k), id: Trunc[i32](i)))
        }
        l.SortStableInPlace(fn(x: &W8, y: &W8): bool {
            c.Store(c.Load() + 1)
            return x.k < y.k
        })
        for x in l.Items() {
            h = (h ^ Trunc[u64](x.id)) *% 1099511628211
        }
    } else if w == 40 {
        var l := HeapList[W40].New(a)
        defer l.Free(a)
        for i in 0..n {
            try l.Push(a, W40(k: keys[i].k, id: i, a: i, b: 0, c: 0))
        }
        l.SortStableInPlace(fn(x: &W40, y: &W40): bool {
            c.Store(c.Load() + 1)
            return x.k < y.k
        })
        for x in l.Items() {
            h = (h ^ Trunc[u64](x.id)) *% 1099511628211
        }
    } else if w == 16 {
        var l := HeapList[WA16].New(a)
        defer l.Free(a)
        for i in 0..n {
            try l.Push(a, WA16(k: keys[i].k, id: i))
        }
        l.SortStableInPlace(fn(x: &WA16, y: &WA16): bool {
            c.Store(c.Load() + 1)
            return x.k < y.k
        })
        for x in l.Items() {
            h = (h ^ Trunc[u64](x.id)) *% 1099511628211
        }
    } else if w == 4096 {
        var l := HeapList[W4096].New(a)
        defer l.Free(a)
        for i in 0..n {
            try l.Push(a, W4096(k: keys[i].k, id: i, pad: [0; 510]))
        }
        l.SortStableInPlace(fn(x: &W4096, y: &W4096): bool {
            c.Store(c.Load() + 1)
            return x.k < y.k
        })
        for x in l.Items() {
            h = (h ^ Trunc[u64](x.id)) *% 1099511628211
        }
    } else {
        var l := HeapList[WBig].New(a)
        defer l.Free(a)
        for i in 0..n {
            try l.Push(a, WBig(k: keys[i].k, id: i, pad: [0; 511]))
        }
        l.SortStableInPlace(fn(x: &WBig, y: &WBig): bool {
            c.Store(c.Load() + 1)
            return x.k < y.k
        })
        for x in l.Items() {
            h = (h ^ Trunc[u64](x.id)) *% 1099511628211
        }
    }
    cmps := c.Load()
    try Print(out, "{w} {pat} {n} {h} {cmps}\n", (w, pat, n, h, cmps))
}
`

// sortCacheCase is one (element width, pattern, size) case and its cache.
type sortCacheCase struct{ w, pat, n, cache int }

func sortCacheCases() []sortCacheCase {
	var cs []sortCacheCase
	for _, w := range []struct{ w, cache int }{{8, 512}, {40, 102}, {16, 0}} {
		for _, n := range blkSizes(70, 1<<12) {
			for pat := range blkPatternNames {
				cs = append(cs, sortCacheCase{w.w, pat, n, w.cache})
			}
		}
	}
	for _, w := range []struct{ w, cache int }{{4096, 1}, {4104, 0}} {
		for _, n := range blkSizes(40, 600) {
			for _, pat := range []int{0, 2, 3, 7, 10} {
				cs = append(cs, sortCacheCase{w.w, pat, n, w.cache})
			}
		}
	}
	return cs
}

// TestSortCacheSizesMatchModel: SortStableInPlace of elements of 8, 40, 16
// (aligned to 16), 4,096 and 4,104 bytes, every pattern of blkPattern at
// every n to 70 and the block and buffer boundaries to 4,096 (to 600 for
// the 4 KiB elements), gives slices.SortStableFunc's order and the model's
// count of less calls with the cache that size gets, at dev and release.
// std-coll_blockmerge_test.go holds the 16-byte cache (256) to 2^20.
func TestSortCacheSizesMatchModel(t *testing.T) {
	t.Parallel()
	cs := sortCacheCases()
	var src, want strings.Builder
	src.WriteString(blockMergeHead)
	src.WriteString(sortCacheSizesExtra)
	fmt.Fprintf(&src, "\nconst Cases: [%d]int = [", 3*len(cs))
	for i, c := range cs {
		if i > 0 {
			src.WriteString(", ")
		}
		fmt.Fprintf(&src, "%d, %d, %d", c.w, c.pat, c.n)
		keys := blkPattern(c.pat, c.n)
		ids := make([]int, c.n)
		for j, x := range blkWant(keys) {
			ids[j] = x.id
		}
		m := blkRunModel(keys, false, c.cache)
		fmt.Fprintf(&want, "%d %d %d %d %d\n", c.w, c.pat, c.n, idDigest(ids), m.cmps)
	}
	src.WriteString("]\n\nexport Main(sys: &System): OutOfMemory|WriteError!void {\n    var out := sys.Stdout()\n    a := sys.heap.Alloc()\n")
	fmt.Fprintf(&src, "    for i in 0..%d {\n        try RunW(mut out, a, Cases[3 * i], Cases[3 * i + 1], Cases[3 * i + 2])\n    }\n}\n", len(cs))
	t.Logf("%d cases", len(cs))
	for _, flags := range [][]string{nil, {"--release"}} {
		diffLines(t, collExec(t, collBuildWith(t, nil, src.String(), flags...)), want.String())
	}
}
