# sha256 (idiomatic)

**Task.** SHA-256 hashing over a generated multi-MiB buffer exercising both bulk throughput and short-message hashing. `sha256 <mib> <bulk_reps> <short_reps> <seed>`: generate `mib * 1048576` bytes from SplitMix64 (`seed`, 8 bytes little-endian per step). Run `bulk_reps` full-buffer SHA-256 passes (XOR-folding each digest's first 8 little-endian bytes into `sum` and XORing `buf[r % n]` with the low byte of the digest), followed by `short_reps` passes hashing every 64-byte slice `buf[j..j+64]` (XOR-folding each digest into `sum` and XORing `buf[(r * 997) % n]` with the low byte of `sum` after each pass). Print `sum`.

**Cells.**

| Column | SHA-256 implementation |
|---|---|
| mk | `std.hash.Sha256` (one-shot; runtime SHA-NI dispatch when present) |
| zig-fast, zig-safe | `std.crypto.hash.sha2.Sha256.hash` |
| go | `crypto/sha256.Sum256` (runtime SHA-NI dispatch when present) |
| cs, cs-aot | `System.Security.Cryptography.SHA256.HashData` (zero-alloc Span overload) |
| c | no std SHA-256 in C |
| rust | no std SHA-256 in Rust |

Checksums: quick `1 4 1 1` `4640190249815661836`, publish `16 150 15 1` `2496816022547215231`. Overflow class `wraps`.
