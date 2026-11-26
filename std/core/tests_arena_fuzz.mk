// Why: FreeOwnChunk returns chunks to the source when freed through
// Alloc(), so a randomized ledger fuzz verifies no live block is ever
// freed or overwritten across nested arenas and resets.
// What: TaFzLedger, TaFzState, the TaFz* fuzz harness and the frame/Alloc
// fuzz test block (split from tests_arena.mk).
// How: runs 192 deterministic seeds against a poisoning ledger heap under
// mk test --std=std.core.

import std.raw

// A fuzz of FreeOwnChunk, the free that returns a chunk to the source: a
// chunk returned while a block in it lives is a use after free. The root's
// source is a ledger that poisons each block it is given back and refuses
// a free of anything it did not give; every live block's bytes and every
// header's chunk list are checked after each operation.

// The ledger: each block live at the source, with its size and alignment.
// A free of anything else counts as bad and is not forwarded; a block freed
// is filled with 0xDD first, so a chunk returned with a live block in it
// shows in that block's bytes.
struct TaFzLedger {
    inner: *Alloc
    p: [1024]*u8
    size: [1024]int
    align: [1024]int
    n: int
    allocs: int
    frees: int
    bad: int
    qp: [64]*u8
    qsize: [64]int
    qalign: [64]int
    qn: int
}

// TaFzQuit gives the source the oldest quarantined block, or all of them.
TaFzQuit(l: *TaFzLedger, all: bool) {
    unsafe "l is the test's live TaFzLedger; its quarantine holds freed blocks" {
        for (*l).qn > 0 && (all || (*l).qn == 64) {
            FreeBytes(&*(*l).inner, (*l).qp[0], (*l).qsize[0], (*l).qalign[0])
            n := (*l).qn - 1
            for j in 0..n {
                (*l).qp[j] = (*l).qp[j + 1]
                (*l).qsize[j] = (*l).qsize[j + 1]
                (*l).qalign[j] = (*l).qalign[j + 1]
            }
            (*l).qn = n
        }
    }
}

// Eight poison bytes read as one word.
const TaFzPoison = -2459565876494606883

TaFzLAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is the test's live TaFzLedger" {
        l := raw.PtrCast[u8, TaFzLedger](ctx)
        p := AllocBytes(&*(*l).inner, size, align)
        if raw.IsNull(p) {
            return p
        }
        if (*l).n == 1024 {
            (*l).bad += 1000
            return p
        }
        i := (*l).n
        (*l).p[i] = p
        (*l).size[i] = size
        (*l).align[i] = align
        (*l).n = i + 1
        (*l).allocs += 1
        return p
    }
}

TaFzLResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

TaFzLFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is the test's live TaFzLedger" {
        l := raw.PtrCast[u8, TaFzLedger](ctx)
        var at := -1
        for i in 0..(*l).n {
            if raw.Same((*l).p[i], p) {
                at = i
            }
        }
        if at < 0 || (*l).size[at] != size || (*l).align[at] != align {
            (*l).bad += 1
            return
        }
        raw.Fill(p, 0xDD, size)
        n := (*l).n - 1
        (*l).p[at] = (*l).p[n]
        (*l).size[at] = (*l).size[n]
        (*l).align[at] = (*l).align[n]
        (*l).n = n
        (*l).frees += 1
        // Quarantined, so the poison stays readable until the checks after
        // this operation; the source gets it 64 frees later.
        TaFzQuit(l, false)
        q := (*l).qn
        (*l).qp[q] = p
        (*l).qsize[q] = size
        (*l).qalign[q] = align
        (*l).qn = q + 1
    }
}

const TaFzLTable = AllocTable(alloc: TaFzLAlloc, resize: TaFzLResize, free: TaFzLFree)

// TaFzHas reports whether the ledger holds p with size bytes at align.
TaFzHas(l: *TaFzLedger, p: *u8, size: int, align: int): bool {
    unsafe "l is the test's live TaFzLedger" {
        for i in 0..(*l).n {
            if raw.Same((*l).p[i], p) {
                return (*l).size[i] == size && (*l).align[i] == align
            }
        }
    }
    return false
}

// The blocks the fuzz holds: from an arena's Alloc() (typed false), or a
// typed value of the root's (Put, Slice, SliceAligned), never freed.
struct TaFzState {
    p: [160]*u8
    size: [160]int
    align: [160]int
    tag: [160]u8
    owner: [160]int
    typed: [160]bool
    from: [160]*Alloc
    n: int
    seed: u64
    ids: int
    bad: int
    ops: int
    frees: int
    rets: int
    arets: int
    ledger: *TaFzLedger
}

TaFzRand(sp: *TaFzState, n: int): int {
    unsafe "sp is the test's live state" {
        s := (*sp).seed *% 6364136223846793005 +% 1442695040888963407
        (*sp).seed = s
        return Trunc[int](s >> 33) % n
    }
}

TaFzBad(sp: *TaFzState, why: int) {
    unsafe "sp is the test's live state" {
        if (*sp).bad == 0 {
            (*sp).bad = why
        }
    }
}

TaFzOk(sp: *TaFzState): bool {
    unsafe "sp is the test's live state" {
        return (*sp).bad == 0
    }
}

// TaFzCheck samples every held block: its first and last byte and every
// 509th must still be its tag.
TaFzCheck(sp: *TaFzState) {
    if !TaFzOk(sp) {
        return
    }
    unsafe "sp is the test's live state; its blocks are live" {
        for i in 0..(*sp).n {
            p := (*sp).p[i]
            n := (*sp).size[i]
            t := (*sp).tag[i]
            var j := 0
            for j < n {
                if raw.Read(raw.Offset(p, j)) != t {
                    TaFzBad(sp, 10)
                }
                j += 509
            }
            if n > 0 && raw.Read(raw.Offset(p, n - 1)) != t {
                TaFzBad(sp, 11)
            }
        }
    }
}

// TaFzHdr checks an arena's chunk list: CurBit only on the head, never
// with OwnBit; OwnBit only on an aligned chunk; an ordinary chunk's top
// byte 0; the last chunk the first (the header right after its record);
// the current free bytes in one listed chunk; and, for the root, every
// chunk a live ledger block of its size and alignment.
TaFzHdr(sp: *TaFzState, a: &Arena, root: bool) {
    if !TaFzOk(sp) {
        return
    }
    unsafe "a's header is live" {
        h := a.hdr
        var c := (*h).chunks
        var k := 0
        var base := false
        for !raw.IsNull(c) {
            k += 1
            if k > 100000 {
                TaFzBad(sp, 20)
                return
            }
            sz := (*c).size
            // A chunk record the ledger poisoned: a chunk of the arena
            // under this one was returned with this one's chunk in it.
            if sz == TaFzPoison || raw.Read(raw.PtrCast[Chunk, int](c)) == TaFzPoison {
                TaFzBad(sp, 28)
                return
            }
            sh := ChunkShift(c)
            if (sz & CurBit) != 0 && (k != 1 || (sz & OwnBit) != 0 || sh == 0) {
                TaFzBad(sp, 21)
            }
            if (sz & OwnBit) != 0 && sh == 0 {
                TaFzBad(sp, 22)
            }
            if sh == 0 && (sz >> ShiftAt) != 0 {
                TaFzBad(sp, 23)
            }
            if raw.IsNull((*c).prev) {
                if !raw.Same(raw.Offset(raw.PtrCast[Chunk, u8](c), SizeOf[Chunk]()), raw.PtrCast[ArenaHdr, u8](h)) {
                    TaFzBad(sp, 24)
                }
            }
            if sh == 0 && IsCurrent(h, c) {
                base = true
            }
            if root {
                var al := ChunkSrc(h)
                if sh != 0 {
                    al = 1 << sh
                }
                if !TaFzHas((*sp).ledger, raw.PtrCast[Chunk, u8](c), sz & SizeMask(), al) {
                    TaFzBad(sp, 25)
                }
            }
            c = (*c).prev
        }
        if !base {
            TaFzBad(sp, 26)
        }
        if root && k != (*(*sp).ledger).n {
            TaFzBad(sp, 27)
        }
    }
}

TaFzAdd(sp: *TaFzState, p: *u8, size: int, align: int, owner: int, typed: bool, from: *Alloc) {
    unsafe "sp is the test's live state; p a fresh block of size bytes" {
        i := (*sp).n
        t := Trunc[u8]((*sp).ops % 200 + 1)
        raw.Fill(p, t, size)
        (*sp).p[i] = p
        (*sp).size[i] = size
        (*sp).align[i] = align
        (*sp).tag[i] = t
        (*sp).owner[i] = owner
        (*sp).typed[i] = typed
        (*sp).from[i] = from
        (*sp).n = i + 1
    }
}

// TaFzRemove drops entry i, keeping the order of the rest.
TaFzRemove(sp: *TaFzState, i: int) {
    unsafe "sp is the test's live state" {
        n := (*sp).n - 1
        for j in i..n {
            (*sp).p[j] = (*sp).p[j + 1]
            (*sp).size[j] = (*sp).size[j + 1]
            (*sp).align[j] = (*sp).align[j + 1]
            (*sp).tag[j] = (*sp).tag[j + 1]
            (*sp).owner[j] = (*sp).owner[j + 1]
            (*sp).typed[j] = (*sp).typed[j + 1]
            (*sp).from[j] = (*sp).from[j + 1]
        }
        (*sp).n = n
    }
}

// TaFzDrop forgets the blocks of an arena about to end.
TaFzDrop(sp: *TaFzState, owner: int) {
    unsafe "sp is the test's live state" {
        var i := 0
        for i < (*sp).n {
            if (*sp).owner[i] == owner {
                TaFzRemove(sp, i)
            } else {
                i += 1
            }
        }
    }
}

// TaFzRoom is the room left for a block at align in a's current chunk,
// or in its current aligned chunk for an align over ChunkAlign: a size that
// fills it to its end.
TaFzRoom(a: &Arena, align: int): int {
    unsafe "a's header is live" {
        h := a.hdr
        if align <= ChunkAlign {
            return (*h).size - AlignUp((*h).used, align)
        }
        c := (*h).chunks
        if !IsAlignedCur(c) || align > (1 << ChunkShift(c)) {
            return -1
        }
        ac := raw.PtrCast[Chunk, AlignedChunk](c)
        return ((*ac).size & SizeMask()) - AlignUp((*ac).used, align)
    }
}

// TaFzAlign: a third at most ChunkAlign, a third from 16 to 65536, and a
// third the alignment of a's current aligned chunk (an aligned block there
// can fill it to its end: the shape a returned chunk must not take).
TaFzAlign(sp: *TaFzState, a: &Arena): int {
    k := TaFzRand(sp, 3)
    if k == 0 {
        return 1 << TaFzRand(sp, 4)
    }
    if k == 2 {
        unsafe "a's header is live" {
            c := (*a.hdr).chunks
            if IsAlignedCur(c) {
                return 1 << ChunkShift(c)
            }
        }
    }
    return 1 << (4 + TaFzRand(sp, 13))
}

// TaFzSize: zero, small, medium, a power of two from 4 KiB to 1 MiB, the
// room left at align (a block to its chunk's end), a little more than that
// room (a new chunk with little in it), or over 500,000 bytes (a chunk of
// its own, on either side of the 512 KiB line).
TaFzSize(sp: *TaFzState, a: &Arena, align: int): int {
    cls := TaFzRand(sp, 100)
    if cls < 8 {
        return 0
    }
    if cls < 34 {
        return TaFzRand(sp, 200)
    }
    if cls < 48 {
        return TaFzRand(sp, 9000)
    }
    if cls < 58 {
        return 4096 << TaFzRand(sp, 9)
    }
    r := TaFzRoom(a, align)
    if cls < 76 {
        if r >= 0 {
            return r
        }
        return TaFzRand(sp, 5000)
    }
    if cls < 90 {
        if r >= 0 {
            return r + 1 + TaFzRand(sp, 100)
        }
        return TaFzRand(sp, 5000)
    }
    return 500000 + TaFzRand(sp, 700000)
}

// One operation on arena a (id owner): an allocation from its Alloc(), a
// free of one of its blocks (the last or any), a resize of its last block
// to its chunk's end, or, on the root, a typed value.
TaFzOp(sp: *TaFzState, a: &Arena, owner: int, isroot: bool) {
    if !TaFzOk(sp) {
        return
    }
    op := TaFzRand(sp, 100)
    unsafe "sp is the test's live state" {
        (*sp).ops += 1
        if (*sp).n >= 150 {
            // Full: free instead.
            TaFzFreeOne(sp, owner, true)
            return
        }
        if op < 42 {
            align := TaFzAlign(sp, a)
            size := TaFzSize(sp, a, align)
            if size > 1200000 {
                return
            }
            p := AllocBytes(a.Alloc(), size, align)
            if raw.IsNull(p) {
                TaFzBad(sp, 30)
                return
            }
            TaFzAdd(sp, p, size, align, owner, false, raw.Of(a.Alloc()))
            return
        }
        if op < 72 {
            TaFzFreeOne(sp, owner, TaFzRand(sp, 2) == 0)
            return
        }
        if op < 84 {
            var i := (*sp).n - 1
            for i >= 0 && ((*sp).owner[i] != owner || (*sp).typed[i]) {
                i -= 1
            }
            if i < 0 {
                return
            }
            old := (*sp).size[i]
            al := (*sp).align[i]
            if TaFzRoom(a, al) < 0 {
                return
            }
            // To its chunk's end from the block's end, and one byte either
            // side: only the last block, the one that ends where the free
            // bytes start, resizes in place.
            var room := 0
            if al <= ChunkAlign {
                h := a.hdr
                room = (*h).size - (*h).used
            } else {
                ac := raw.PtrCast[Chunk, AlignedChunk]((*a.hdr).chunks)
                room = ((*ac).size & SizeMask()) - (*ac).used
            }
            var new := old + room + TaFzRand(sp, 3) - 1
            if new < 0 {
                new = 0
            }
            p := (*sp).p[i]
            if ResizeBytes(a.Alloc(), p, old, new, al) {
                if new > old {
                    raw.Fill(raw.Offset(p, old), (*sp).tag[i], new - old)
                }
                (*sp).size[i] = new
            }
            return
        }
        if !isroot {
            return
        }
        TaFzTyped(sp, a, owner)
    }
}

// TaFzFreeOne frees a block of owner's: its last one, or any.
TaFzFreeOne(sp: *TaFzState, owner: int, last: bool) {
    unsafe "sp is the test's live state" {
        var cand := 0
        for i in 0..(*sp).n {
            if (*sp).owner[i] == owner && !(*sp).typed[i] {
                cand += 1
            }
        }
        if cand == 0 {
            return
        }
        var pick := cand - 1
        if !last {
            pick = TaFzRand(sp, cand)
        }
        for i in 0..(*sp).n {
            if (*sp).owner[i] == owner && !(*sp).typed[i] {
                if pick == 0 {
                    l := (*sp).ledger
                    f0 := (*l).frees
                    FreeBytes(&*(*sp).from[i], (*sp).p[i], (*sp).size[i], (*sp).align[i])
                    if (*l).frees > f0 {
                        (*sp).rets += 1
                        if (*sp).align[i] > ChunkAlign {
                            (*sp).arets += 1
                        }
                    }
                    (*sp).frees += 1
                    TaFzRemove(sp, i)
                    return
                }
                pick -= 1
            }
        }
    }
}

// A typed value of the root's: an align(64) Put, a SliceAligned at any
// alignment, or a Slice (a line one on a line arena when 4 KiB or more).
TaFzTyped(sp: *TaFzState, a: &Arena, owner: int) {
    k := TaFzRand(sp, 3)
    if k == 0 {
        q := a.Put(TaLine(a: 1)) else {
            TaFzBad(sp, 31)
            return
        }
        unsafe "q lives in the root until its Free" {
            TaFzAdd(sp, raw.PtrCast[TaLine, u8](raw.OfMut(q)), 64, 64, owner, true, raw.Of(a.Alloc()))
        }
        return
    }
    if k == 1 {
        al := 1 << TaFzRand(sp, 17)
        var n := TaFzRand(sp, 6000)
        r := TaFzRoom(a, al)
        if TaFzRand(sp, 2) == 0 && r >= 0 {
            n = r
        }
        s := a.SliceAligned[u8](n, al) else {
            TaFzBad(sp, 32)
            return
        }
        unsafe "s lives in the root until its Free" {
            TaFzAdd(sp, raw.DataMut(s), n, al, owner, true, raw.Of(a.Alloc()))
        }
        return
    }
    n := 4096 + TaFzRand(sp, 20000)
    s := a.Slice[u8](n) else {
        TaFzBad(sp, 33)
        return
    }
    unsafe "s lives in the root until its Free" {
        TaFzAdd(sp, raw.DataMut(s), n, 1, owner, true, raw.Of(a.Alloc()))
    }
}

TaFzNewId(sp: *TaFzState): int {
    unsafe "sp is the test's live state" {
        (*sp).ids += 1
        return (*sp).ids
    }
}

// TaFzAll checks the blocks and the root's chunk list.
TaFzAll(sp: *TaFzState, root: &Arena) {
    TaFzCheck(sp)
    TaFzHdr(sp, root, true)
}

// TaFzForget ends an arena whose chunk list a failed check found broken,
// without walking it.
TaFzForget(a: Arena) {
    Arena(hdr: p) := a
    unsafe "a failed run: the arena's chunks are left to the process" {
        raw.Forget(p)
    }
}

// A level: two frames f and g on under (the root or a frame), and h, a
// Sub of f (an arena on under's Alloc() too, which borrows f); operations
// on the root, under, f, g and h in any order, a Reset of g, deeper levels
// on f or g, and the three freed in one of three orders (h before f). Every
// header is checked after each step; after a failed check nothing more
// runs and nothing is freed.
TaFzLevel(sp: *TaFzState, root: &Arena, under: &Arena, uid: int, level: int, steps: int) {
    var f := Arena.NewIn(under.Alloc()) else {
        TaFzBad(sp, 40)
        return
    }
    var g := Arena.NewIn(under.Alloc()) else {
        TaFzBad(sp, 41)
        f.Free()
        return
    }
    var h := f.Sub() else {
        TaFzBad(sp, 42)
        g.Free()
        f.Free()
        return
    }
    fid := TaFzNewId(sp)
    gid := TaFzNewId(sp)
    hid := TaFzNewId(sp)
    var step := 0
    for step < steps && TaFzOk(sp) {
        step += 1
        w := TaFzRand(sp, 100)
        if w < 13 {
            TaFzOp(sp, root, 0, true)
        } else if w < 30 {
            TaFzOp(sp, under, uid, uid == 0)
        } else if w < 50 {
            TaFzOp(sp, &f, fid, false)
        } else if w < 70 {
            TaFzOp(sp, &g, gid, false)
        } else if w < 82 {
            TaFzOp(sp, &h, hid, false)
        } else if w < 85 {
            TaFzDrop(sp, gid)
            g.Reset()
        } else if w < 93 {
            if level < 3 {
                if TaFzRand(sp, 2) == 0 {
                    TaFzLevel(sp, root, &f, fid, level + 1, steps / 2)
                } else {
                    TaFzLevel(sp, root, &g, gid, level + 1, steps / 2)
                }
            }
        } else if TaFzRand(sp, 3) == 0 {
            step = steps
        }
        TaFzAll(sp, root)
        TaFzHdr(sp, under, uid == 0)
        TaFzHdr(sp, &f, false)
        TaFzHdr(sp, &g, false)
        TaFzHdr(sp, &h, false)
    }
    if !TaFzOk(sp) {
        TaFzForget(h)
        TaFzForget(f)
        TaFzForget(g)
        return
    }
    k := TaFzRand(sp, 3)
    if k == 0 {
        TaFzDrop(sp, hid)
        h.Free()
        TaFzAll(sp, root)
        TaFzDrop(sp, fid)
        f.Free()
        TaFzAll(sp, root)
        TaFzHdr(sp, &g, false)
        TaFzDrop(sp, gid)
        g.Free()
    } else if k == 1 {
        TaFzDrop(sp, hid)
        h.Free()
        TaFzAll(sp, root)
        TaFzDrop(sp, gid)
        g.Free()
        TaFzAll(sp, root)
        TaFzHdr(sp, &f, false)
        TaFzDrop(sp, fid)
        f.Free()
    } else {
        TaFzDrop(sp, gid)
        g.Free()
        TaFzAll(sp, root)
        TaFzHdr(sp, &h, false)
        TaFzDrop(sp, hid)
        h.Free()
        TaFzAll(sp, root)
        TaFzDrop(sp, fid)
        f.Free()
    }
    TaFzAll(sp, root)
    TaFzHdr(sp, under, uid == 0)
}

// TaFzRounds runs rounds of levels on root, with a Reset of the root
// after some, then frees it (or, after a failed check, forgets it).
TaFzRounds(sp: *TaFzState, root: Arena, rounds: int, steps: int) {
    var r := root
    for _ in 0..rounds {
        if TaFzOk(sp) {
            TaFzLevel(sp, &r, &r, 0, 0, steps)
        }
        if TaFzOk(sp) && TaFzRand(sp, 3) == 0 {
            TaFzDrop(sp, 0)
            r.Reset()
            TaFzAll(sp, &r)
        }
    }
    TaFzDrop(sp, 0)
    if TaFzOk(sp) {
        r.Free()
    } else {
        TaFzForget(r)
    }
}

// TaFzRun runs one seed on a root over a fresh ledger: a line arena
// (Arena.New on the ledger's heap) or an arena by NewIn. It returns the
// first failure's code: 0 when every check held and the ledger is empty
// after the root's Free.
TaFzRun(sys: &System, seed: u64, line: bool, rounds: int, steps: int, stats: mut [4]int): int {
    var l := TaFzLedger(inner: raw.Of(sys.heap.Alloc()), p: [raw.Null[u8](); 1024], size: [0; 1024], align: [0; 1024], n: 0, allocs: 0, frees: 0, bad: 0, qp: [raw.Null[u8](); 64], qsize: [0; 64], qalign: [0; 64], qn: 0)
    lp := raw.OfMut(mut l)
    var st := TaFzState(p: [raw.Null[u8](); 160], size: [0; 160], align: [0; 160], tag: [0; 160], owner: [0; 160], typed: [false; 160], from: [raw.Null[Alloc](); 160], n: 0, seed: seed, ids: 0, bad: 0, ops: 0, frees: 0, rets: 0, arets: 0, ledger: lp)
    sp := raw.OfMut(mut st)
    unsafe "l and st are this frame's, live while the arenas are" {
        ctx := raw.PtrCast[TaFzLedger, u8](lp)
        var hp := MakeHeap(ctx, raw.Of(&TaFzLTable))
        la := MakeAlloc(ctx, &TaFzLTable)
        if line {
            root := Arena.New(&hp) else {
                return 1
            }
            TaFzRounds(sp, root, rounds, steps)
        } else {
            root := Arena.NewIn(&la) else {
                return 2
            }
            TaFzRounds(sp, root, rounds, steps)
        }
        TaFzQuit(lp, true)
        stats[0] += (*sp).ops
        stats[1] += (*sp).frees
        stats[2] += (*sp).rets
        stats[3] += (*sp).arets
        if (*sp).bad != 0 {
            return (*sp).bad
        }
        if (*lp).bad != 0 {
            return 50
        }
        if (*lp).n != 0 || (*lp).allocs != (*lp).frees {
            return 51
        }
    }
    return 0
}

// 192 seeds, alternating a line root and a NewIn one. Seeds 539492,
// 777062, 840414, 1204688 and 1283878 (k 68, 98, 106, 152 and 162) find R1
// (mem_arena.mk at 24aa785e). Frees through an Alloc() return over 100
// ordinary and over 100 aligned chunks to the source across the seeds, so
// the path under test runs.
test "fuzz: frames and Alloc() frees on an arena keep every live block" (sys: &System) {
    var fails := 0
    var stats: [4]int = [0; 4]
    for k in 0..192 {
        seed := Trunc[u64](1000 + k * 7919)
        code := TaFzRun(sys, seed, k % 2 == 0, 6, 60, mut stats)
        if code != 0 {
            fails += 1
            try sys.Print("seed {s} line {l}: code {c}\n", (s: seed, l: k % 2 == 0, c: code))
        }
    }
    Assert(fails == 0 && stats[2] - stats[3] > 100 && stats[3] > 100)
}
