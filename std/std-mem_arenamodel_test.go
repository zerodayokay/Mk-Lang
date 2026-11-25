// Why: the arena's chunk policy (Bump, BumpSlow, the AllocTable entries, Put,
// Slice, Reset, Free) has many paths that ordinary tests never reach.
// What: a differential test against a Go model (amModel), and planted bugs.
// How: a seeded operation sequence runs as a const initializer and natively
// at dev and release; digest checkpoints must equal the model's.

package std

import (
	"fmt"
	"strings"
	"testing"
)

// arenaModelFixture is std.core's fixture: it reads the arena's private
// header, which only std.core can.
const arenaModelFixture = `import std.raw

const AmHdr = 64
const AmTable = AllocTable(alloc: AmAlloc, resize: AmResize, free: AmFree)

// The model heap's counters at ctx: 0 the bump offset, 1 allocations,
// 2 frees, 3 live bytes, 4 the memory's size; blocks follow at AmHdr in
// 16-byte units. free only counts, so freed memory never comes back.
AmWord(ctx: *u8, i: int): *i64 {
    unsafe "ctx is the model heap's memory; its first 40 bytes are the counters" {
        return raw.PtrCast[u8, i64](raw.Offset(ctx, i * 8))
    }
}

AmGet(ctx: *u8, i: int): i64 {
    unsafe "a counter of the model heap" {
        return raw.Read(AmWord(ctx, i))
    }
}

AmSet(ctx: *u8, i: int, v: i64) {
    unsafe "a counter of the model heap" {
        raw.Write(AmWord(ctx, i), v)
    }
}

AmAlloc(ctx: *u8, size: int, align: int): *u8 {
    off := int(AmGet(ctx, 0))
    room := int(AmGet(ctx, 4)) - AmHdr - off
    if size < 0 || size > room {
        return raw.Null[u8]()
    }
    need := (size + 15) / 16 * 16
    if need > room {
        return raw.Null[u8]()
    }
    AmSet(ctx, 0, i64(off + need))
    AmSet(ctx, 1, AmGet(ctx, 1) + 1)
    AmSet(ctx, 3, AmGet(ctx, 3) + i64(size))
    unsafe "inside the model heap's memory" {
        return raw.Offset(ctx, AmHdr + off)
    }
}

AmResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

AmFree(ctx: *u8, p: *u8, size: int, align: int) {
    AmSet(ctx, 2, AmGet(ctx, 2) + 1)
    AmSet(ctx, 3, AmGet(ctx, 3) - i64(size))
}

AmNext(s: u64): u64 {
    return s *% 6364136223846793005 +% 1442695040888963407
}

AmFnv(h: u64, v: i64): u64 {
    return (h ^ Trunc[u64](v)) *% 1099511628211
}

AmChunks(h: *ArenaHdr): i64 {
    var n: i64 = 0
    unsafe "h is a live arena header" {
        var c := (*h).chunks
        for !raw.IsNull(c) {
            n += 1
            c = (*c).prev
        }
    }
    return n
}

AmDigest(hs: u64, h: *ArenaHdr, ctx: *u8, kind: i64, res: i64): u64 {
    var x := AmFnv(AmFnv(hs, kind), res)
    unsafe "h is a live arena header" {
        x = AmFnv(x, i64((*h).used))
        x = AmFnv(x, i64((*h).size))
        x = AmFnv(x, i64((*h).next & ~LineBit))
    }
    x = AmFnv(x, AmChunks(h))
    for i in 0..4 {
        x = AmFnv(x, AmGet(ctx, i))
    }
    return x
}

AmCheck(p: *u8, size: int, t: u8): bool {
    unsafe "p is a live block of size bytes" {
        for j in 0..size {
            if raw.Read(raw.Offset(p, j)) != t {
                return false
            }
        }
    }
    return true
}

AmFill(p: *u8, size: int, t: u8) {
    unsafe "p is a live block of size bytes" {
        raw.Fill(p, t, size)
    }
}

AmPut(a: &Arena, v: u64): *u8 {
    p := a.Put[u64](v) else {
        return raw.Null[u8]()
    }
    unsafe "the slot lives in the arena until its Reset or Free" {
        return raw.PtrCast[u64, u8](raw.OfMut(p))
    }
}

AmSlice(a: &Arena, m: int): *u8 {
    s := a.Slice[u32](m) else {
        return raw.Null[u8]()
    }
    unsafe "the slice lives in the arena until its Reset or Free" {
        return raw.PtrCast[u32, u8](raw.DataMut(s))
    }
}

// ArenaModel runs ops seeded operations on an arena over a model heap of
// bytes bytes at mem (16-aligned) and writes the digest after every 16
// operations to out, and the digest after Free to out[ops / 16]. It returns
// the number of bad checks (a block whose bytes changed, a Slice not zero).
export ArenaModel(mem: *u8, bytes: int, seed: u64, ops: int, out: mut []u64): i64 {
    AmFill(mem, AmHdr, 0)
    AmSet(mem, 4, i64(bytes))
    var heap := MakeHeap(mem, raw.Of(&AmTable))
    var a := Arena.New(&heap) else {
        return -1
    }
    h := a.hdr
    var ptr: [256]*u8 = [raw.Null[u8](); 256]
    var siz: [256]int = [0; 256]
    var tag: [256]u8 = [0; 256]
    var n := 0
    var s := seed
    var hs: u64 = 14695981039346656037
    var bad: i64 = 0
    for i in 0..ops {
        s = AmNext(s)
        op := Trunc[int](s >> 33) % 100
        s = AmNext(s)
        r1 := Trunc[int](s >> 33)
        s = AmNext(s)
        r2 := Trunc[int](s >> 33)
        t := Trunc[u8](i)
        var kind := 10
        if op < 45 {
            kind = 1
        } else if op < 50 {
            kind = 2
        } else if op < 65 {
            kind = 3
        } else if op < 72 {
            kind = 4
        } else if op < 84 {
            kind = 5
        } else if op < 90 {
            kind = 6
        } else if op < 94 {
            kind = 7
        } else if op < 98 {
            kind = 8
        } else if op == 98 {
            kind = 9
        }
        if (kind == 1 || kind == 9 || kind == 10) && n == 256 {
            kind = 8
        }
        if kind >= 3 && kind <= 6 && n == 0 {
            kind = 0
        }
        var res: i64 = 0
        if kind == 1 {
            cls := r1 % 16
            var size := r2 % 65
            if cls == 15 {
                size = 2000 + r2 % 40000
            } else if cls >= 13 {
                size = r2 % 9000
            } else if cls >= 8 {
                size = r2 % 3000
            }
            var align := 1
            for _ in 0..(r1 / 16) % 4 {
                align = align * 2
            }
            p := AllocBytes(a.Alloc(), size, align)
            if !raw.IsNull(p) {
                res = 1
                AmFill(p, size, t)
                ptr[n] = p
                siz[n] = size
                tag[n] = t
                n += 1
            }
        } else if kind == 2 {
            which := r1 % 4
            var size := 8
            var align := 8
            if which == 0 {
                size = -1
            } else if which == 1 {
                align = 0
            } else if which == 2 {
                align = 3
            } else {
                // Past MaxAlign (65,536): one over ChunkAlign is served from
                // an aligned chunk of its own, which the model does
                // not cover.
                align = 131072
            }
            if !raw.IsNull(AllocBytes(a.Alloc(), size, align)) {
                res = 1
            }
        } else if kind == 3 || kind == 4 {
            var j := n - 1
            if kind == 4 {
                j = r1 % n
            }
            old := siz[j]
            var new := r2 % (2 * old + 64)
            if kind == 3 && r1 % 4 == 0 {
                // To the chunk's end, one short of it or one past it.
                unsafe "h is a live arena header" {
                    new = old + ((*h).size - (*h).used) + r2 % 3 - 1
                }
                if new < 0 {
                    new = 0
                }
            }
            if ResizeBytes(a.Alloc(), ptr[j], old, new, 1) {
                res = 1
                unsafe "the block grew in place to new bytes" {
                    if new > old {
                        AmFill(raw.Offset(ptr[j], old), new - old, tag[j])
                    }
                }
                siz[j] = new
            }
        } else if kind == 5 || kind == 6 {
            var j := n - 1
            if kind == 6 {
                j = r1 % n
            }
            FreeBytes(a.Alloc(), ptr[j], siz[j], 1)
            for m in j..n - 1 {
                ptr[m] = ptr[m + 1]
                siz[m] = siz[m + 1]
                tag[m] = tag[m + 1]
            }
            n -= 1
        } else if kind == 7 {
            which := r1 % 4
            if n == 0 || which == 2 {
                FreeBytes(a.Alloc(), raw.Null[u8](), 8, 1)
            } else if which == 3 {
                unsafe "h is a live arena header" {
                    FreeBytes(a.Alloc(), ptr[n - 1], (*h).used + 1, 1)
                }
            } else {
                var j := n - 1
                if which == 1 {
                    j = r2 % n
                }
                if siz[j] > 1 {
                    FreeBytes(a.Alloc(), ptr[j], siz[j] - 1, 1)
                }
            }
        } else if kind == 8 {
            for j in 0..n {
                if !AmCheck(ptr[j], siz[j], tag[j]) {
                    bad += 1
                }
            }
            a.Reset()
            n = 0
        } else if kind == 9 {
            p := AmPut(&a, Trunc[u64](r2))
            if !raw.IsNull(p) {
                res = 1
                AmFill(p, 8, t)
                ptr[n] = p
                siz[n] = 8
                tag[n] = t
                n += 1
            }
        } else if kind == 10 {
            m := r2 % 300
            p := AmSlice(&a, m)
            if !raw.IsNull(p) {
                res = 1
                if !AmCheck(p, 4 * m, 0) {
                    bad += 1
                }
                AmFill(p, 4 * m, t)
                ptr[n] = p
                siz[n] = 4 * m
                tag[n] = t
                n += 1
            }
        }
        hs = AmDigest(hs, h, mem, i64(kind), res)
        if i % 32 == 31 {
            for j in 0..n {
                if !AmCheck(ptr[j], siz[j], tag[j]) {
                    bad += 1
                }
            }
        }
        if i % 16 == 15 {
            hs = AmFnv(hs, bad)
            out[i / 16] = hs
        }
    }
    for j in 0..n {
        if !AmCheck(ptr[j], siz[j], tag[j]) {
            bad += 1
        }
    }
    a.Free()
    hs = AmFnv(hs, bad)
    hs = AmFnv(AmFnv(AmFnv(hs, AmGet(mem, 1)), AmGet(mem, 2)), AmGet(mem, 3))
    out[ops / 16] = hs
    return bad
}

// The const leg: 480 operations over 32 KiB, which runs out part way.
export ArenaModelSmall(): [31]u64 {
    var mem: [4096]u64 = [0; 4096]
    var out: [31]u64 = [0; 31]
    unsafe "mem is 32 KiB of this frame, 8-aligned" {
        _ = ArenaModel(raw.PtrCast[u64, u8](raw.DataMut(mut mem[..])), 32768, 7, 480, mut out[..])
    }
    return out
}

const AmSmallC = ArenaModelSmall() fuel 400_000_000

export ArenaModelSmallConst(): [31]u64 {
    return AmSmallC
}

// The native legs: bytes bytes of model heap from h.
export ArenaModelHeap(h: &Heap, bytes: int, seed: u64, ops: int, out: mut []u64): i64 {
    p := AllocBytes(h.Alloc(), bytes, 16)
    if raw.IsNull(p) {
        return -2
    }
    r := ArenaModel(p, bytes, seed, ops, out)
    FreeBytes(h.Alloc(), p, bytes, 16)
    return r
}
`

const arenaModelModule = `import std.core

export Const(): [31]u64 {
    return core.ArenaModelSmallConst()
}

export Small(): [31]u64 {
    return core.ArenaModelSmall()
}

export Run(h: &Heap, bytes: int, seed: u64, ops: int, out: mut []u64): i64 {
    return core.ArenaModelHeap(h, bytes, seed, ops, out)
}
`

// arenaModelRuns are the native runs after the two small legs: a heap
// large enough that most of the run has memory, and one that runs out.
var arenaModelRuns = []struct {
	bytes int64
	seed  uint64
	ops   int
}{{32 << 20, 11, 20000}, {192 << 10, 12, 8000}}

func arenaModelMain() string {
	var b strings.Builder
	b.WriteString(`import std.arenamodel

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    for x in arenamodel.Const() {
        try Print(mut out, "c{x}\n", (x))
    }
    for x in arenamodel.Small() {
        try Print(mut out, "s{x}\n", (x))
    }
`)
	for k, r := range arenaModelRuns {
		fmt.Fprintf(&b, `    var o%[1]d: [%[2]d]u64 = [0; %[2]d]
    r%[1]d := arenamodel.Run(sys.heap, %[3]d, %[4]d, %[5]d, mut o%[1]d[..])
    try Print(mut out, "r%[1]d={r%[1]d}\n", (r%[1]d))
    for x in o%[1]d[..] {
        try Print(mut out, "b%[1]d.{x}\n", (x))
    }
`, k, r.ops/16+1, r.bytes, r.seed, r.ops)
	}
	b.WriteString("}\n")
	return b.String()
}

// The Go model of the documented policy (core/mem_arena.mk). Constants as the file states them: the first chunk is 4,096
// bytes and holds the 16-byte chunk record and the 64-byte header, so its
// free bytes start at 80; an ordinary chunk's start at 16; next starts at
// 8,192 and doubles up to 1 MiB; a request over next/2 (or over next - 16)
// gets a chunk of its own and the current chunk stays current, and next
// grows then too (amGrow).
const (
	amFirst    = 4096
	amHdrRoom  = 80
	amRoom     = 16
	amMaxChunk = 1 << 20
	amHeapHdr  = 64
)

type amChunk struct {
	id, size int
	first    bool
}

type amBlock struct{ chunk, off, size int }

type amModel struct {
	heapCap, heapOff, allocs, frees, live int64
	chunks                                []amChunk // newest first
	cur, used, size, next, ids            int
	// oldOwnChunk replays an older policy (an own chunk left next
	// unchanged), so the growth property test can show it fails.
	oldOwnChunk bool
}

func (m *amModel) heapAlloc(size int) bool {
	room := m.heapCap - amHeapHdr - m.heapOff
	if size < 0 || int64(size) > room {
		return false
	}
	need := int64(size+15) / 16 * 16
	if need > room {
		return false
	}
	m.heapOff += need
	m.allocs++
	m.live += int64(size)
	return true
}

func (m *amModel) heapFree(size int) {
	m.frees++
	m.live -= int64(size)
}

func (m *amModel) newChunk(size int, first bool) int {
	m.ids++
	m.chunks = append([]amChunk{{id: m.ids, size: size, first: first}}, m.chunks...)
	return m.ids
}

// bump is Bump and BumpSlow: the block's chunk and offset from the chunk's
// free bytes, or ok false.
func (m *amModel) bump(size, align int) (amBlock, bool) {
	off := (m.used + align - 1) &^ (align - 1)
	if size <= m.size-off {
		m.used = off + size
		return amBlock{m.cur, off, size}, true
	}
	if size > m.next/2 || size > m.next-amRoom {
		if !m.heapAlloc(amRoom + size) {
			return amBlock{}, false
		}
		c := m.newChunk(amRoom+size, false)
		if !m.oldOwnChunk {
			m.next = amGrow(m.next, size)
		}
		return amBlock{c, 0, size}, true
	}
	if !m.heapAlloc(m.next) {
		return amBlock{}, false
	}
	c := m.newChunk(m.next, false)
	m.size = m.next - amRoom
	if m.next < amMaxChunk {
		m.next *= 2
	}
	m.cur, m.used = c, size
	return amBlock{c, 0, size}, true
}

// amGrow is next after an own chunk for a request of size bytes: doubled once, then until size <= next/2, never past
// amMaxChunk.
func amGrow(next, size int) int {
	if next < amMaxChunk {
		next *= 2
	}
	for next < amMaxChunk && size > next/2 {
		next *= 2
	}
	return next
}

func (m *amModel) alloc(size, align int) (amBlock, bool) {
	if size < 0 || align < 1 || align > 8 || align&(align-1) != 0 {
		return amBlock{}, false
	}
	return m.bump(size, align)
}

// last reports whether p + size is where the free bytes start.
func (m *amModel) last(b amBlock, size int) bool {
	return b.chunk == m.cur && b.off+size == m.used
}

func (m *amModel) resize(b amBlock, old, new int) bool {
	if old < 0 || new < 0 || old > m.used || !m.last(b, old) {
		return false
	}
	start := m.used - old
	if new > m.size-start {
		return false
	}
	m.used = start + new
	return true
}

func (m *amModel) free(b amBlock, size int, null bool) {
	if size <= 0 || null {
		return
	}
	if size <= m.used && m.last(b, size) {
		m.used -= size
		return
	}
	// FreeOwnChunk: the newest chunk goes back when the block has it to
	// itself (never the first or the current chunk).
	if c := m.chunks[0]; !c.first && c.id != m.cur && c.id == b.chunk && b.off == 0 && c.size == amRoom+size {
		m.chunks = m.chunks[1:]
		m.heapFree(c.size)
	}
}

func (m *amModel) reset() {
	var first, cur *amChunk
	for i := range m.chunks {
		c := &m.chunks[i]
		switch {
		case c.first:
			first = c
		case cur == nil && c.id == m.cur:
			cur = c
		default:
			m.heapFree(c.size)
		}
	}
	if cur == nil {
		m.chunks = []amChunk{*first}
		m.cur, m.used, m.size = first.id, 0, amFirst-amHdrRoom
		return
	}
	m.chunks = []amChunk{*cur, *first}
	m.cur, m.used, m.size = cur.id, 0, cur.size-amRoom
}

func (m *amModel) digest(hs uint64, kind, res int64) uint64 {
	x := fnvStep(fnvStep(hs, uint64(kind)), uint64(res))
	for _, v := range []int64{int64(m.used), int64(m.size), int64(m.next), int64(len(m.chunks)), m.heapOff, m.allocs, m.frees, m.live} {
		x = fnvStep(x, uint64(v))
	}
	return x
}

// amRun is ArenaModel in Go: the checkpoints, the final digest, and how
// many times each kind of operation ran and how many succeeded (so the test
// can show that the run reached every path).
func amRun(bytes int64, seed uint64, ops int) (out []uint64, ran, ok [11]int) {
	m := &amModel{heapCap: bytes}
	out = make([]uint64, ops/16+1)
	if !m.heapAlloc(amFirst) {
		panic("model heap smaller than the first chunk")
	}
	m.cur = m.newChunk(amFirst, true)
	m.size, m.next = amFirst-amHdrRoom, 2*amFirst
	var live []amBlock
	next := func(s uint64) uint64 { return s*6364136223846793005 + 1442695040888963407 }
	s := seed
	hs := uint64(fnvStart)
	for i := 0; i < ops; i++ {
		s = next(s)
		op := int(s>>33) % 100
		s = next(s)
		r1 := int(s >> 33)
		s = next(s)
		r2 := int(s >> 33)
		kind := 10
		switch {
		case op < 45:
			kind = 1
		case op < 50:
			kind = 2
		case op < 65:
			kind = 3
		case op < 72:
			kind = 4
		case op < 84:
			kind = 5
		case op < 90:
			kind = 6
		case op < 94:
			kind = 7
		case op < 98:
			kind = 8
		case op == 98:
			kind = 9
		}
		n := len(live)
		if (kind == 1 || kind == 9 || kind == 10) && n == 256 {
			kind = 8
		}
		if kind >= 3 && kind <= 6 && n == 0 {
			kind = 0
		}
		res := int64(0)
		add := func(size, align int) {
			if b, good := m.alloc(size, align); good {
				res = 1
				live = append(live, b)
			}
		}
		switch kind {
		case 1:
			cls := r1 % 16
			size := r2 % 65
			switch {
			case cls == 15:
				size = 2000 + r2%40000
			case cls >= 13:
				size = r2 % 9000
			case cls >= 8:
				size = r2 % 3000
			}
			add(size, 1<<((r1/16)%4))
		case 2:
			// Every invalid request is null and changes nothing.
		case 3, 4:
			j := n - 1
			if kind == 4 {
				j = r1 % n
			}
			old := live[j].size
			nw := r2 % (2*old + 64)
			if kind == 3 && r1%4 == 0 {
				nw = max(old+(m.size-m.used)+r2%3-1, 0)
			}
			if m.resize(live[j], old, nw) {
				res = 1
				live[j].size = nw
			}
		case 5, 6:
			j := n - 1
			if kind == 6 {
				j = r1 % n
			}
			m.free(live[j], live[j].size, false)
			live = append(live[:j], live[j+1:]...)
		case 7:
			switch which := r1 % 4; {
			case n == 0 || which == 2:
				m.free(amBlock{}, 8, true)
			case which == 3:
				m.free(live[n-1], m.used+1, false)
			default:
				j := n - 1
				if which == 1 {
					j = r2 % n
				}
				if live[j].size > 1 {
					m.free(live[j], live[j].size-1, false)
				}
			}
		case 8:
			m.reset()
			live = live[:0]
		case 9:
			add(8, 8)
		case 10:
			add(4*(r2%300), 4)
		}
		ran[kind]++
		if res == 1 {
			ok[kind]++
		}
		hs = m.digest(hs, int64(kind), res)
		if i%16 == 15 {
			hs = fnvStep(hs, 0)
			out[i/16] = hs
		}
	}
	for _, c := range m.chunks {
		m.heapFree(c.size)
	}
	hs = fnvStep(hs, 0)
	hs = fnvStep(fnvStep(fnvStep(hs, uint64(m.allocs)), uint64(m.frees)), uint64(m.live))
	out[ops/16] = hs
	return out, ran, ok
}

// arenaModelWant is the program's expected output.
func arenaModelWant(t *testing.T) []string {
	var lines []string
	small, ran, ok := amRun(32768, 7, 480)
	t.Logf("small: ran %v, succeeded %v", ran, ok)
	for _, p := range []string{"c", "s"} {
		for _, x := range small {
			lines = append(lines, fmt.Sprintf("%s%d", p, x))
		}
	}
	for k, r := range arenaModelRuns {
		out, ran, ok := amRun(r.bytes, r.seed, r.ops)
		t.Logf("run %d (%d bytes, seed %d): ran %v, succeeded %v", k, r.bytes, r.seed, ran, ok)
		lines = append(lines, fmt.Sprintf("r%d=0", k))
		for _, x := range out {
			lines = append(lines, fmt.Sprintf("b%d.%d", k, x))
		}
	}
	return lines
}

// arenaModelDiff reports the first line where got and want differ, or "".
func arenaModelDiff(got string, want []string) string {
	g := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	for i := range want {
		if i >= len(g) {
			return fmt.Sprintf("output ends after %d lines, want %d", len(g), len(want))
		}
		if g[i] != want[i] {
			return fmt.Sprintf("line %d: %s, want %s (a checkpoint covers 16 operations)", i+1, g[i], want[i])
		}
	}
	if len(g) != len(want) {
		return fmt.Sprintf("%d lines, want %d", len(g), len(want))
	}
	return ""
}

// TestStdMem_ArenaModel: every checkpoint of every run, const and native,
// at dev and release, equals the Go model's, and every run reached each
// kind of operation, with both outcomes where an operation can fail.
//
// A seeded sequence of operations (allocations of mixed sizes and
// alignments through Arena.Alloc(), invalid requests, resizes and frees of
// the last block and of any block, a quarter of the last-block resizes to
// the chunk's end or one byte either side of it, frees with a wrong size or
// null, Put, Slice, Reset) runs on an arena over a counting bump heap of a
// fixed size, so the heap runs out part way and the null paths run too.
// After every operation the arena's state (used, size, next, the chunk
// count) and the heap's four counters go into a digest, and every 16
// operations the digest is a checkpoint. Every live block holds its own tag
// byte and is checked every 32 operations and before each Reset, so an
// overlap or a stale block shows in the digest. A wrong size is one byte
// short, or past the bump position: both must change nothing. A size one
// byte long is not used, because it can be legal by accident: when the
// alignment padding after a block is one byte and the block after it was
// freed, the free position is the block's end plus one, so the arena
// reclaims the live block. The AllocTable contract gives free the block's
// own size, and the arena trusts it, as bumpalo's dealloc trusts its
// Layout. The same Mk function runs in the evaluator and natively, so this
// is also a parity test of the arena code between the two.
func TestStdMem_ArenaModel(t *testing.T) {
	t.Parallel()
	want := arenaModelWant(t)
	for k, r := range arenaModelRuns {
		_, ran, ok := amRun(r.bytes, r.seed, r.ops)
		for kind := 1; kind <= 10; kind++ {
			if ran[kind] == 0 {
				t.Errorf("run %d never ran operation kind %d", k, kind)
			}
		}
		for _, kind := range []int{1, 3, 4, 9, 10} {
			if ok[kind] == 0 {
				t.Errorf("run %d: operation kind %d never succeeded", k, kind)
			}
		}
		if k == 1 && ok[1] == ran[1] {
			t.Errorf("run %d: every allocation succeeded; the small heap should run out", k)
		}
	}
	fix := map[string]string{"core/tests_arenamodel.mk": arenaModelFixture, "arenamodel/arenamodel.mk": arenaModelModule}
	for _, flags := range [][]string{nil, {"--release"}} {
		r := runNative(t, buildNative(t, fix, arenaModelMain(), flags...), "")
		if r.code != 0 {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", flags, r.code, r.stdout, r.stderr)
		}
		if d := arenaModelDiff(r.stdout, want); d != "" {
			t.Errorf("%v: %s", flags, d)
		}
	}
}

// TestStdMem_ArenaModelSeesPlantedBugs: each planted change to the policy
// moves a checkpoint, so the model checks what it claims to; and with fuel
// 1000 the const leg stops at E0902, so it really runs in the evaluator.
func TestStdMem_ArenaModelSeesPlantedBugs(t *testing.T) {
	t.Parallel()
	want := arenaModelWant(t)
	t.Run("const leg runs in the evaluator", func(t *testing.T) {
		t.Parallel()
		low := strings.Replace(arenaModelFixture, "fuel 400_000_000", "fuel 1000", 1)
		fix := map[string]string{"core/tests_arenamodel.mk": low, "arenamodel/arenamodel.mk": arenaModelModule}
		out, code := checkTree(t, fix, arenaModelMain())
		if code == 0 || !strings.Contains(out, "E0902") {
			t.Errorf("mk check with fuel 1000: exit %d, want E0902\n%s", code, out)
		}
	})
	src := readText(t, "core/mem_arena.mk")
	for _, p := range []struct{ name, old, new string }{
		{"free never reclaims", "(*h).used = (*h).used - size", "(*h).used = (*h).used"},
		{"own chunk past a quarter", "if size > next / 2 ||", "if size > next / 4 ||"},
		{"Reset keeps only the first chunk", "} else if raw.IsNull(cur) && IsCurrent(h, c) {", "} else if false {"},
		{"resize grows past the chunk", "if new > (*h).size - start {", "if new > (*h).size - start + 8 {"},
		{"own chunk leaves next", "            (*h).next = n\n            return raw.Offset", "            return raw.Offset"},
	} {
		planted := strings.Replace(src, p.old, p.new, 1)
		if planted == src {
			t.Fatalf("%s: line %q not found in core/mem_arena.mk", p.name, p.old)
		}
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			fix := map[string]string{"core/tests_arenamodel.mk": arenaModelFixture, "arenamodel/arenamodel.mk": arenaModelModule, "core/mem_arena.mk": planted}
			r := runNative(t, buildNative(t, fix, arenaModelMain()), "")
			if r.code == 0 && arenaModelDiff(r.stdout, want) == "" {
				t.Errorf("the planted change left every checkpoint equal to the model's")
			}
		})
	}
}
