# hashmap-heap (idiomatic)

**Task.** The same hash map insert/lookup/delete churn task as `idiomatic/hashmap`, but with **every language using its default general-purpose heap/GC allocator** (no arena allocator in Mk or Zig). `hashmap-heap <n> <rounds> <seed>`.

**Cells:**

| Column | Map | Allocator | Hash | DoS posture |
|---|---|---|---|---|
| mk | `HeapMap[u64, u64].New(sys.heap.Alloc())` | `sys.heap.Alloc()` (general heap) | DD-5 fast fixed hash | none: public fixed key |
| mk-keyed | `HeapMap[u64, u64].NewKeyed(sys.heap.Alloc(), key)` | `sys.heap.Alloc()` (general heap) | SipHash-1-3 under 16 random bytes per process | keyed |
| rust, rust-ovf | `HashMap::new()` | global allocator (`System`) | SipHash-1-3 under `RandomState` | keyed |
| zig-fast, zig-safe | `std.AutoHashMap(u64, u64).init(init.gpa)` | `std.heap.smp_allocator` (`init.gpa`) | wyhash-based `autoHash`, seed 0 | none: fixed |
| go | `map[uint64]uint64` | Go runtime heap (GC) | runtime AES hash, seeded per map | seeded |
| cs, cs-aot | `Dictionary<ulong, ulong>` | .NET Workstation GC heap | `ulong.GetHashCode()` (fixed/unkeyed) | none: fixed |
| c | no std hash map in C | | | |

Checksums: quick `100000 3 1` `15000450000`, publish `3000000 3 1` `13500013500000`. Overflow class `wraps`.
