// Why: Arena and Box are the allocation primitives the rest of std builds on.
// What: arena lifecycle, AllocTable edge, Box and arena tests, and benchmarks.
// How: each test is a compile-time fixture over a counting heap (expectClean)
// or a native program (buildNative) judged against a Go model; the
// benchmarks (MK_BENCH=1) time release builds.

package std

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// countingHeap is a fixture heap over a byte array of the caller's frame. Its
// first 32 bytes are four i64 counters: the bump offset, allocations, frees
// and live bytes; blocks follow, 16-aligned. free only counts, so a block's
// memory is never reused and a stale pointer would read stale bytes, not
// another block's.
const countingHeap = `import std.raw

const CountBytes = 65536
const CountTable = AllocTable(alloc: CountAlloc, resize: CountResize, free: CountFree)

CountWord(ctx: *u8, i: int): *i64 {
    unsafe "ctx is the fixture's byte array; its first 32 bytes are the counters" {
        return raw.PtrCast[u8, i64](raw.Offset(ctx, i * 8))
    }
}

CountGet(ctx: *u8, i: int): i64 {
    unsafe "a counter of the fixture's array" {
        return raw.Read(CountWord(ctx, i))
    }
}

CountAdd(ctx: *u8, i: int, n: i64) {
    unsafe "a counter of the fixture's array" {
        raw.Write(CountWord(ctx, i), raw.Read(CountWord(ctx, i)) + n)
    }
}

CountAlloc(ctx: *u8, size: int, align: int): *u8 {
    off := int(CountGet(ctx, 0))
    need := (size + 15) / 16 * 16
    if 32 + off + need > CountBytes {
        return raw.Null[u8]()
    }
    CountAdd(ctx, 0, i64(need))
    CountAdd(ctx, 1, 1)
    CountAdd(ctx, 3, i64(size))
    unsafe "inside the fixture's array" {
        return raw.Offset(ctx, 32 + off)
    }
}

CountResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

CountFree(ctx: *u8, p: *u8, size: int, align: int) {
    CountAdd(ctx, 2, 1)
    CountAdd(ctx, 3, i64(0 - size))
}
`

// TestArenaLifecycleCounted runs the arena's code at compile time over
// the counting heap: New is one heap allocation (the header lives in the
// first chunk); Put gives fresh disjoint memory; Slice zero-fills and
// fails with OutOfMemory, not an overflow panic, on a byte count past int;
// a request over half a chunk gets its own chunk; Alloc() is one owner;
// Sub is independent; Reset keeps the first and the current chunk, so a
// repeated workload reaches a cycle with no heap allocation; Free returns
// every byte.
func TestArenaLifecycleCounted(t *testing.T) {
	t.Parallel()
	expectClean(t, "mem_life", countingHeap+`
Cycle(a: &Arena, k: int): int {
    var bad := 0
    for i in 0..10 {
        var buf: [1000]u8 = [0; 1000]
        buf[0] = Trunc[u8](65 + (i + k) % 26)
        buf[999] = Trunc[u8](97 + i)
        s := str.From(buf[..]) else { return 100 }
        d := a.Dup(s) else { return 101 }
        if d.Len() != 1000 || d.Byte(0) != buf[0] || d.Byte(999) != buf[999] {
            bad += 1
        }
    }
    return bad
}

HugeFails(a: &Arena): bool {
    _ = a.Slice[i64](9223372036854775807) else { return true }
    return false
}

ArenaCounted(): int {
    var mem: [65536]u8 = [0; 65536]
    unsafe "fixture" {
        ctx := raw.DataMut(mut mem[..])
        heap := MakeHeap(ctx, raw.Of(&CountTable))
        var a := Arena.New(&heap) else { return 90 }
        var bad := 0
        if CountGet(ctx, 1) != 1 {
            bad += 1 // New is one allocation
        }
        p := a.Put(41) else {
            a.Free()
            return 91
        }
        q := a.Put(42) else {
            a.Free()
            return 92
        }
        if *p != 41 || *q != 42 || raw.Same(raw.OfMut(p), raw.OfMut(q)) {
            bad += 1
        }
        *p = 7
        if *q != 42 {
            bad += 1 // disjoint
        }
        zs := a.Slice[i64](5) else {
            a.Free()
            return 93
        }
        for i in 0..zs.Len() {
            if zs[i] != 0 {
                bad += 1
            }
        }
        if !HugeFails(&a) {
            bad += 1 // a byte count past int is OutOfMemory
        }
        if !raw.Same(raw.Of(a.Alloc()), raw.Of(a.Alloc())) {
            bad += 1
        }
        before := CountGet(ctx, 1)
        big := a.Slice[u8](5000) else {
            a.Free()
            return 94
        }
        if big.Len() != 5000 || CountGet(ctx, 1) != before + 1 {
            bad += 1 // its own chunk, one allocation
        }
        small := a.Dup("after") else {
            a.Free()
            return 95
        }
        if small != "after" || CountGet(ctx, 1) != before + 1 {
            bad += 1 // the current chunk stayed current
        }
        var b := a.Sub() else {
            a.Free()
            return 96
        }
        t := b.Dup("sub") else {
            b.Free()
            a.Free()
            return 97
        }
        if t != "sub" || small != "after" {
            bad += 1
        }
        b.Free()
        var counts: [6]i64 = [0; 6]
        start := CountGet(ctx, 1)
        for k in 0..6 {
            bad += Cycle(&a, k)
            counts[k] = CountGet(ctx, 1)
            a.Reset()
        }
        if counts[3] != counts[2] || counts[4] != counts[2] || counts[5] != counts[2] {
            bad += 1 // a steady state with no heap traffic
        }
        if counts[0] == start {
            bad += 1 // the workload outgrows the first chunk (since
            // The 5,000-byte own chunk grew next, so cycle 0's
            // new chunk already holds a whole cycle and cycle 1 allocates
            // nothing)
        }
        a.Free()
        if CountGet(ctx, 2) != CountGet(ctx, 1) || CountGet(ctx, 3) != 0 {
            bad += 1 // every allocation freed, every byte returned
        }
        return bad
    }
}

const MemLife = [0; 1][ArenaCounted()]
`)
}

// TestArenaAllocTableEdges: the AllocTable entries behind
// Arena.Alloc() at compile time: a negative size, a zero alignment, an
// alignment that is not a power of two and one over 8 are null (the
// table's failure value), never a panic; resize grows and shrinks the last
// block in place and refuses any other; free gives back the last block of
// the current chunk only (a LIFO pair unwinds in turn) and ignores a wrong
// size, null or another block.
func TestArenaAllocTableEdges(t *testing.T) {
	t.Parallel()
	expectClean(t, "mem_table", countingHeap+`
TableEdges(): int {
    var mem: [65536]u8 = [0; 65536]
    unsafe "fixture" {
        ctx := raw.DataMut(mut mem[..])
        heap := MakeHeap(ctx, raw.Of(&CountTable))
        var a := Arena.New(&heap) else { return 90 }
        var bad := 0
        al := a.Alloc()
        if !raw.IsNull(AllocBytes(al, -1, 8)) || !raw.IsNull(AllocBytes(al, 8, 0)) {
            bad += 1
        }
        // Past MaxAlign; 16 to 65,536 is served aligned.
        if !raw.IsNull(AllocBytes(al, 8, 3)) || !raw.IsNull(AllocBytes(al, 8, 131072)) {
            bad += 1
        }
        x := AllocBytes(al, 10, 8)
        y := AllocBytes(al, 10, 8)
        if raw.IsNull(x) || raw.IsNull(y) {
            bad += 1
        }
        if ResizeBytes(al, x, 10, 20, 8) {
            bad += 1 // not the last block
        }
        if !ResizeBytes(al, y, 10, 30, 8) || !ResizeBytes(al, y, 30, 4, 8) {
            bad += 1
        }
        if ResizeBytes(al, y, 4, 1000000, 8) {
            bad += 1 // past the chunk
        }
        z := AllocBytes(al, 1, 1)
        if !raw.Same(z, raw.Offset(y, 4)) {
            bad += 1 // the shrink gave the bytes back
        }
        FreeBytes(al, z, 1, 1)
        z2 := AllocBytes(al, 1, 1)
        if !raw.Same(z2, z) {
            bad += 1 // freeing the last block gave its byte back
        }
        FreeBytes(al, z2, 2, 1)
        FreeBytes(al, z2, 0, 1)
        FreeBytes(al, z2, -1, 1)
        FreeBytes(al, raw.Null[u8](), 1, 1)
        FreeBytes(al, x, 10, 8)
        z3 := AllocBytes(al, 1, 1)
        if !raw.Same(z3, raw.Offset(z2, 1)) {
            bad += 1 // a wrong size, a null and a block not last give nothing back
        }
        FreeBytes(al, z3, 1, 1)
        FreeBytes(al, z2, 1, 1)
        if !raw.Same(AllocBytes(al, 1, 1), z2) {
            bad += 1 // LIFO frees unwind in turn
        }
        allocs := CountGet(ctx, 1)
        a.Free()
        if CountGet(ctx, 2) != allocs {
            bad += 1
        }
        return bad
    }
}

const MemTable = [0; 1][TableEdges()]
`)
}

// boxProgram exercises Box natively: a recursive list of Box[Node] (the
// [0]&T marker; [0]T made Node a layout cycle, E0907), New, Ref, Mut, Take,
// Free, a zero-size T, and a Box[Box[int]] taken apart.
const boxProgram = `linear struct Node {
    v: int
    next: ?Box[Node]
}

struct Unit {
}

Sum(a: &Alloc, head: ?Box[Node]): int {
    var total := 0
    var cur := head
    for {
        match cur {
            none { return total }
            some(b) {
                Node(v, next) := b.Take(a)
                total = total + v
                cur = next
            }
        }
    }
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    a := sys.heap.Alloc()
    var head: ?Box[Node] = none
    for i in 1..1001 {
        head = some(Box[Node].New(a, Node(v: i, next: head)).Must())
    }
    s := Sum(a, head)
    var b := Box[int].New(a, 5).Must()
    *b.Mut() = *b.Ref() + 1
    six := *b.Ref()
    b.Free(a)
    u := Box[Unit].New(a, Unit()).Must()
    _ = u.Take(a)
    bb := Box[Box[int]].New(a, Box[int].New(a, 9).Must()).Must()
    inner := bb.Take(a)
    nine := inner.Take(a)
    try Print(mut out, "{s} {six} {nine}\n", (s, six, nine))
}
`

// TestBoxNative: boxProgram prints the list's sum and the values, at
// dev and at release.
func TestBoxNative(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{nil, {"--release"}} {
		r := runNative(t, buildNative(t, nil, boxProgram, flags...), "")
		wantRun(t, r, 0, "500500 6 9\n")
	}
}

// TestBoxPanics: the allocator-identity check (panic code 12) on Take
// and Free with another allocator, each in its own process: exit 101 and the
// message. Free of a linear T that is not Free is E0701.
func TestBoxPanics(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, body, want string }{
		{"take", "b := Box[int].New(sys.heap.Alloc(), 1).Must()\n    _ = b.Take(sys.arena.Alloc())", "wrong allocator: Box.Take"},
		{"free", "b := Box[int].New(sys.heap.Alloc(), 1).Must()\n    b.Free(sys.arena.Alloc())", "wrong allocator: Box.Free"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := "linear struct Tok {\n    n: int\n}\n\nexport Main(sys: &System) {\n    " + c.body + "\n}\n"
			r := runNative(t, buildNative(t, nil, src), "")
			if r.code != 101 || !strings.Contains(r.stderr, c.want) {
				t.Errorf("exit %d, want 101 with %q\nstderr:\n%s", r.code, c.want, r.stderr)
			}
		})
	}
}

// arenaProgram puts n values of mixed sizes into one arena (crossing many
// chunks, with a large slice that gets its own chunk), checks every value
// after all were written (fresh, disjoint memory), then resets and
// repeats; it prints a checksum the Go model computes.
const arenaProgram = `struct Pair {
    a: u8
    b: i64
}

Fill(ar: &Arena, n: int, seed: u64): OutOfMemory!u64 {
    var xs := List[mut Pair].New(ar)
    var s := seed
    for i in 0..n {
        s = s *% 6364136223846793005 +% 1442695040888963407
        p := try ar.Put(Pair(a: Trunc[u8](s >> 56), b: Trunc[i64](s >> 1)))
        try xs.Push(p)
        if i % 1000 == 999 {
            big := try ar.Slice[u8](300000)
            big[299999] = 1
        }
    }
    var sum: u64 = 0
    var t := seed
    for i in 0..xs.Len() {
        t = t *% 6364136223846793005 +% 1442695040888963407
        pa := xs.Items()[i].a
        pb := xs.Items()[i].b
        if pa != Trunc[u8](t >> 56) || pb != Trunc[i64](t >> 1) {
            return 0
        }
        sum = sum +% Trunc[u64](pb) +% u64(pa)
    }
    return sum
}

export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    for round in 0..3 {
        s := Fill(&ar, 20000, Trunc[u64](round) + 1) else {
            ar.Free()
            return
        }
        Print(mut out, "{s}\n", (s)) else {
            ar.Free()
            return
        }
        ar.Reset()
    }
    ar.Free()
}
`

// goArenaSums is arenaProgram's output computed in Go.
func goArenaSums() string {
	var b strings.Builder
	for round := 0; round < 3; round++ {
		t := uint64(round) + 1
		var sum uint64
		for i := 0; i < 20000; i++ {
			t = t*6364136223846793005 + 1442695040888963407
			sum += uint64(int64(t>>1)) + uint64(uint8(t>>56))
		}
		fmt.Fprintf(&b, "%d\n", sum)
	}
	return b.String()
}

// TestArenaNative: arenaProgram's checksums equal the Go model's at
// dev and release.
func TestArenaNative(t *testing.T) {
	t.Parallel()
	want := goArenaSums()
	for _, flags := range [][]string{nil, {"--release"}} {
		r := runNative(t, buildNative(t, nil, arenaProgram, flags...), "")
		wantRun(t, r, 0, want)
	}
}

// memBenchProgram times nothing itself; the test times the whole process. Each kernel runs n operations and prints a value
// that depends on all of them, so release cannot drop the work.
func memBenchProgram(kernel string, n int) string {
	switch kernel {
	case "put":
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    var s: u64 = 0
    for i in 0..` + strconv.Itoa(n) + ` {
        p := ar.Put(i) else {
            ar.Free()
            return
        }
        s = s +% Trunc[u64](*p)
    }
    ar.Free()
    try Print(mut out, "{s}\n", (s))
}
`
	case "box":
		return `export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    a := sys.heap.Alloc()
    var s: u64 = 0
    for i in 0..` + strconv.Itoa(n) + ` {
        b := Box[int].New(a, i).Must()
        s = s +% Trunc[u64](b.Take(a))
    }
    try Print(mut out, "{s}\n", (s))
}
`
	case "arenabox":
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    var s: u64 = 0
    a := ar.Alloc()
    for i in 0..` + strconv.Itoa(n) + ` {
        b := Box[int].New(a, i).Must()
        s = s +% Trunc[u64](b.Take(a))
    }
    ar.Free()
    try Print(mut out, "{s}\n", (s))
}
`
	case "empty":
		return `export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var s: u64 = 0
    for i in 0..` + strconv.Itoa(n) + ` {
        s = s +% Trunc[u64](i)
    }
    try Print(mut out, "{s}\n", (s))
}
`
	case "newfree":
		// Arena.New and Arena.Free per operation: one heap block each way.
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var s: u64 = 0
    for i in 0..` + strconv.Itoa(n) + ` {
        var b := try Arena.New(sys.heap)
        p := b.Put(i) else {
            b.Free()
            return
        }
        s = s +% Trunc[u64](*p)
        b.Free()
    }
    try Print(mut out, "{s}\n", (s))
}
`
	case "slice":
		// Arena.Slice[int](8), zero-filled, with a Reset every 256 slices
		// (16 KiB), so the steady state reuses one chunk. The element read
		// is at an index from a generator, so release cannot forward the
		// fill's zero to the load and drop the fill.
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    var s: u64 = 0
    var g: u64 = 1
    for i in 0..` + strconv.Itoa(n) + ` {
        xs := ar.Slice[int](8) else {
            ar.Free()
            return
        }
        g = g *% 6364136223846793005 +% 1442695040888963407
        s = s +% Trunc[u64](i + xs[Trunc[int](g >> 61)])
        if i % 256 == 255 {
            ar.Reset()
        }
    }
    ar.Free()
    try Print(mut out, "{s}\n", (s))
}
`
	case "dup":
		// Arena.Dup of a 32-byte str, each Dup copying the previous copy (so
		// the source is not a constant release can forward), with a Reset
		// every 256 copies (8 KiB, one chunk) that re-seeds from the
		// literal. The byte read is at an index from a generator.
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    lit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    var prev := lit
    var s: u64 = 0
    var g: u64 = 1
    for i in 0..` + strconv.Itoa(n) + ` {
        d := ar.Dup(prev) else {
            ar.Free()
            return
        }
        g = g *% 6364136223846793005 +% 1442695040888963407
        s = s +% Trunc[u64](i + int(d.Byte(Trunc[int](g >> 59))) - 97)
        prev = d
        if i % 256 == 255 {
            prev = lit
            ar.Reset()
        }
    }
    ar.Free()
    try Print(mut out, "{s}\n", (s))
}
`
	case "sub":
		// Arena.Sub, one Put in it, and its Free per operation: Sub is
		// Arena.New on the parent's heap, so this is newfree through Sub.
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    var s: u64 = 0
    for i in 0..` + strconv.Itoa(n) + ` {
        var c := ar.Sub() else {
            ar.Free()
            return
        }
        p := c.Put(i) else {
            c.Free()
            ar.Free()
            return
        }
        s = s +% Trunc[u64](*p)
        c.Free()
    }
    ar.Free()
    try Print(mut out, "{s}\n", (s))
}
`
	case "boxfree":
		// Box[int].New through ar.Alloc(), a read through Ref, then Free:
		// the arena's LIFO free gives the bytes back, so the loop stays in
		// the first chunk and the number is Box's own cost plus two
		// table calls.
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    var s: u64 = 0
    a := ar.Alloc()
    for i in 0..` + strconv.Itoa(n) + ` {
        b := Box[int].New(a, i).Must()
        s = s +% Trunc[u64](*b.Ref())
        b.Free(a)
    }
    ar.Free()
    try Print(mut out, "{s}\n", (s))
}
`
	case "allocfree", "resize":
		// The AllocTable entries through std.core's AllocBytes, ResizeBytes
		// and FreeBytes (the calls every std container makes), in the
		// fixture module std.membench: allocfree is alloc 16 then free;
		// resize grows the last block from 16 to 48 in place between them.
		// resize minus allocfree is ArenaResizeEntry's in-place path.
		fn := "AllocFree"
		if kernel == "resize" {
			fn = "Resize"
		}
		return `import std.membench

export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    s := membench.` + fn + `(ar.Alloc(), ` + strconv.Itoa(n) + `)
    ar.Free()
    try Print(mut out, "{s}\n", (s))
}
`
	case "grow":
		// Put into a fresh arena 65,536 times (512 KiB: chunks of 4 KiB
		// doubling to 512 KiB, so 8 BumpSlow calls), then Free it.
		return `export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var s: u64 = 0
    for c in 0..(` + strconv.Itoa(n) + ` + 65535) / 65536 {
        var b := try Arena.New(sys.heap)
        for j in 0..65536 {
            i := c * 65536 + j
            if i < ` + strconv.Itoa(n) + ` {
                p := b.Put(i) else {
                    b.Free()
                    return
                }
                s = s +% Trunc[u64](*p)
            }
        }
        b.Free()
    }
    try Print(mut out, "{s}\n", (s))
}
`
	}
	panic(kernel)
}

// memBenchModule is the fixture module std.membench: std.core's allocator
// calls are for std modules, not user code. Each function returns the sum
// of i over the rounds that succeeded, so a failed round shows in the sum.
const memBenchModule = `import std.core
import std.raw

export AllocFree(a: &Alloc, n: int): u64 {
    var s: u64 = 0
    for i in 0..n {
        p := core.AllocBytes(a, 16, 8)
        if !raw.IsNull(p) {
            s = s +% Trunc[u64](i)
        }
        core.FreeBytes(a, p, 16, 8)
    }
    return s
}

export Resize(a: &Alloc, n: int): u64 {
    var s: u64 = 0
    for i in 0..n {
        p := core.AllocBytes(a, 16, 8)
        if core.ResizeBytes(a, p, 16, 48, 8) {
            s = s +% Trunc[u64](i)
            core.FreeBytes(a, p, 48, 8)
        } else {
            core.FreeBytes(a, p, 16, 8)
        }
    }
    return s
}
`

// memBenchKernels are TestStdMemBench's kernels, the empty loop first.
var memBenchKernels = []string{"empty", "put", "box", "arenabox", "arenabox0", "newfree", "slice", "grow", "dup", "sub", "boxfree", "allocfree", "resize"}

// TestStdMemBench builds each kernel at release and reports the best of
// five runs per operation, minus the empty loop's. It runs only with
// MK_BENCH=1 (it times processes; record the numbers with the command and
// the machine).
func TestStdMemBench(t *testing.T) {
	if os.Getenv("MK_BENCH") != "1" {
		t.Skip("set MK_BENCH=1 to time the std-mem kernels")
	}
	const n = 10_000_000
	best := map[string]float64{}
	// arenabox0 is arenabox over an ArenaFreeEntry that gives nothing back:
	// every Box takes fresh arena bytes.
	freeEntry := regexp.MustCompile(`(?s)(ArenaFreeEntry\(ctx: \*u8, p: \*u8, size: int, align: int\) \{).*?\n\}\n`)
	arenaSrc := readText(t, "core/mem_arena.mk")
	if !freeEntry.MatchString(arenaSrc) {
		t.Fatal("core/mem_arena.mk: ArenaFreeEntry not found")
	}
	noRollback := map[string]string{"core/mem_arena.mk": freeEntry.ReplaceAllString(arenaSrc, "$1\n}\n")}
	for _, k := range memBenchKernels {
		var fix map[string]string
		src := k
		if k == "arenabox0" {
			fix, src = noRollback, "arenabox"
		}
		if k == "allocfree" || k == "resize" {
			fix = map[string]string{"membench/membench.mk": memBenchModule}
		}
		exe := buildNative(t, fix, memBenchProgram(src, n), "--release")
		want := fmt.Sprintf("%d\n", uint64(n)*(n-1)/2)
		b := -1.0
		for i := 0; i < 5; i++ {
			r := runNative(t, exe, "")
			wantRun(t, r, 0, want)
			if s := r.took.Seconds(); b < 0 || s < b {
				b = s
			}
		}
		best[k] = b
	}
	for _, k := range memBenchKernels[1:] {
		t.Logf("%s: best of 5 %.3f s for %d ops, empty loop %.3f s: %.2f ns/op", k, best[k], n, best["empty"], (best[k]-best["empty"])*1e9/n)
	}
}

// TestStdMem_BenchKernelsRun runs the later kernels (Dup, Sub,
// Box.Free through an arena, and the AllocTable entries with an in-place
// resize) at a small count, at dev and release, so the kernels the bench
// times are checked in every run: each prints the sum of i over the rounds
// that succeeded. 3,000 rounds pass dup's Reset (every 256) eleven times.
func TestStdMem_BenchKernelsRun(t *testing.T) {
	const n = 3000
	want := fmt.Sprintf("%d\n", uint64(n)*(n-1)/2)
	for _, k := range []string{"dup", "sub", "boxfree", "allocfree", "resize"} {
		var fix map[string]string
		if k == "allocfree" || k == "resize" {
			fix = map[string]string{"membench/membench.mk": memBenchModule}
		}
		for _, flags := range [][]string{nil, {"--release"}} {
			t.Run(fmt.Sprintf("%s%v", k, flags), func(t *testing.T) {
				wantRun(t, runNative(t, buildNative(t, fix, memBenchProgram(k, n), flags...), ""), 0, want)
			})
		}
	}
}
