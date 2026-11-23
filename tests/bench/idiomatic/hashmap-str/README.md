# hashmap-str (idiomatic)

**Task.** Insert, lookup and delete churn on each language's standard hash map with its default string hash, short-to-medium ASCII string keys (8–24 bytes) and `u64` values. `hashmap-str <n> <rounds> <seed>`: before the rounds loop, generate `n` distinct present keys and `n` guaranteed-absent miss keys (8–24 lowercase alphanumeric bytes from SplitMix64 into one contiguous buffer, with an invertible 7-byte mixed index prefix plus tag `'k'`/`'m'` at byte 7). Each round creates a fresh empty map (growing from empty, never pre-sized), inserts all `n` present keys with value `i +% r`, probes every key (adding its value on hit, `+ 1` if the corresponding miss key is found, and removing every even index), re-probes all `n` present keys (`+ 3` per remaining key), and adds the final map length. The checksum is the wrapping sum over all rounds.

**Cells.**

| Column | Map | Hash | DoS posture |
|---|---|---|---|
| mk | `Map[str, u64].New` in a sub-arena per round | DD-5 fast fixed hash | none: public fixed key |
| mk-keyed | `Map[str, u64].NewKeyed(&a, HashKey.New(io))` | SipHash-1-3 under 16 random bytes per process | keyed (Rust's, Go's and C#'s peer) |
| rust, rust-ovf | `HashMap<&str, u64>::new()` | SipHash-1-3 under `RandomState` | keyed |
| zig-fast, zig-safe | `std.StringHashMap(u64)`, arena per round | Wyhash, seed 0 | none: fixed |
| go | `map[string]uint64` | runtime AES hash, seeded per map | seeded |
| cs, cs-aot | `Dictionary<string, ulong>` | Marvin32, randomized per process | keyed |
| c | no std hash map in C | | |

Checksums: quick `50000 2 1` `2500200000`, publish `1000000 8 1` `4000040000000`. Overflow class `wraps`.
