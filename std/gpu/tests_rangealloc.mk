// Why: TLSF range allocator bugs cause overlapping GPU suballocations or
// leaked heap space under churn.
//
// What: 100,000-step seeded property test plus alignment and coalescing tests
// for rangealloc.mk.
//
// How: xorshift64 PRNG allocating and freeing aligned blocks from a 64 MiB
// region, checking non-overlap, alignment, and full free-byte recovery.

struct ActiveRange {
    span: RangeSpan
}

RngNext(s: mut u64): u64 {
    var x := *s
    x ^= x << 13
    x ^= x >> 7
    x ^= x << 17
    *s = x
    return x
}

test "rangealloc alignment, splitting and coalescing" (sys: &System) {
    a := sys.heap.Alloc()
    var ra := try RangeAlloc.New(a, 65536)
    defer ra.Free(a)

    s1 := (try ra.Alloc(a, 100, 256)).Must()
    Assert(s1.offset == 0 && s1.size == 100)
    s2 := (try ra.Alloc(a, 200, 4096)).Must()
    Assert(s2.offset == 4096 && s2.size == 200)
    // Padding between 100 and 4096 should be reusable for small aligned alloc.
    s3 := (try ra.Alloc(a, 512, 256)).Must()
    Assert(s3.offset == 256 && s3.size == 512)

    ra.FreeSpan(a, s1.id)
    ra.FreeSpan(a, s2.id)
    ra.FreeSpan(a, s3.id)
    Assert(ra.free_bytes == 65536)

    // Entire region is coalesced into one contiguous span again.
    full := (try ra.Alloc(a, 65536, 4096)).Must()
    Assert(full.offset == 0 && full.size == 65536)
    ra.FreeSpan(a, full.id)
    Assert(ra.free_bytes == 65536)
}

test "rangealloc 100000 seeded alloc and free steps" (sys: &System) {
    a := sys.heap.Alloc()
    total: u64 = 64 * 1024 * 1024
    var ra := try RangeAlloc.New(a, total)
    defer ra.Free(a)

    var active := HeapList[RangeSpan].New(a)
    defer active.Free(a)

    var used_bytes: u64 = 0
    var rng: u64 = 0x9e3779b97f4a7c15
    for _ in 0..100000 {
        r := RngNext(mut rng)
        do_alloc := active.Len() == 0 || (active.Len() < 256 && (r & 3) != 0)
        if do_alloc {
            sz := Cast[u64](1 + Cast[int]((RngNext(mut rng) >> 8) % 65536))
            align_pow := Cast[int]((RngNext(mut rng) >> 4) % 10)
            al := u64(1) << Cast[u64](align_pow)
            match try ra.Alloc(a, sz, al) {
                some(sp) {
                    Assert(sp.offset % al == 0)
                    Assert(sp.offset + sp.size <= total)
                    used_bytes += sp.size
                    try active.Push(a, sp)
                }
                none {}
            }
        } else {
            idx := Cast[int](RngNext(mut rng) % Cast[u64](active.Len()))
            sp := active.Remove(idx)
            used_bytes -= sp.size
            ra.FreeSpan(a, sp.id)
        }
        Assert(ra.free_bytes + used_bytes == total)
    }
    for active.Len() > 0 {
        sp := active.Pop().Must()
        ra.FreeSpan(a, sp.id)
    }
    Assert(ra.free_bytes == total)
    full := (try ra.Alloc(a, total, 65536)).Must()
    Assert(full.offset == 0 && full.size == total)
    ra.FreeSpan(a, full.id)
}
