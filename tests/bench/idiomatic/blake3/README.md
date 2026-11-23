# blake3 (idiomatic)

**Task.** BLAKE3 hashing (32-byte digest) over a generated multi-MiB buffer exercising both bulk throughput and short-message hashing. `blake3 <mib> <bulk_reps> <short_reps> <seed>`: generate `mib * 1048576` bytes from SplitMix64 (`seed`, 8 bytes little-endian per step). Run `bulk_reps` full-buffer BLAKE3 passes (XOR-folding each digest's first 8 little-endian bytes into `sum` and XORing `buf[r % n]` with the low byte of the digest), followed by `short_reps` passes hashing every 64-byte slice `buf[j..j+64]` (XOR-folding each digest into `sum` and XORing `buf[(r * 997) % n]` with the low byte of `sum` after each pass). Print `sum`.

**Cells.**

| Column | BLAKE3 implementation |
|---|---|
| mk | `std.hash.Blake3` (one-shot) |
| zig-fast, zig-safe | `std.crypto.hash.Blake3.hash` |
| c | no std BLAKE3 in C |
| rust | no std BLAKE3 in Rust |
| go | no std BLAKE3 in Go |
| cs, cs-aot | no std BLAKE3 in C# |

Checksums: quick `1 8 2 1` `2377007786082826362`, publish `16 144 18 1` `12663884270560400240`. Overflow class `wraps`.
