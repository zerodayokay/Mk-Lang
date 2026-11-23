# sortstable (idiomatic, stable)

**Task.** The task of `../sort/README.md` (10 M `u64`, four patterns per round, the same fill and the same check) with each language's standard **stable** sort.

**Cells.**

| Column | Sort |
|---|---|
| mk | `Slice[u64].SortStable(sys.heap.Alloc(), Less)` |
| c | no std stable sort in C (`qsort` is the only std sort, and it is not stable) |
| rust, rust-ovf | `sort` (driftsort) |
| zig-fast, zig-safe | `std.sort.block` (block sort, in place) |
| go | `slices.SortStableFunc(xs, cmp.Compare[uint64])` (Go has no stable sort without a comparator; insertion sort of blocks, then SymMerge) |

Stability cannot be seen on bare `u64` keys, so the checksum is the unstable row's; the row prices each library's stable algorithm. Sizes: quick `100000 1 1`, publish `10000000 3 1` (the fastest cell, Rust, ran one round in 0.37 to 0.59 s in a verify run on a shared machine, 2026-11-07). Overflow class `wraps`.
