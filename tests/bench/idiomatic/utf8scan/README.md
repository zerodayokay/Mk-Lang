# utf8scan (idiomatic)

**Task.** Validate a generated mixed-script UTF-8 byte buffer with each language's standard UTF-8 validator, then decode every Unicode scalar value (codepoint) and fold the scalar count and codepoint sum. `utf8scan <n> <rounds>`: repeat a 4-line corpus covering 1-byte ASCII, 2-byte Latin-1/Greek/Cyrillic, 3-byte CJK/Hangul/Kana and 4-byte emoji `n` times into one mutable byte buffer. Each round validates the full buffer (exiting non-zero if invalid), iterates all codepoints accumulating `count` and `cpsum` (wrapping `u64`), folds `cpsum +% count` into `total`, and toggles one leading ASCII byte (`'T'` ↔ `'t'`) via XOR `0x20` so no round can be hoisted.

**Cells.**

| Column | Validation | Codepoint iteration |
|---|---|---|
| mk | `str.From` | `str.Scalars` |
| rust, rust-ovf | `std::str::from_utf8` | `str::chars` |
| zig-fast, zig-safe | `std.unicode.utf8ValidateSlice` | `std.unicode.Utf8View` (`nextCodepoint`) |
| go | `unicode/utf8.Valid` | `for _, c := range string(buf)` |
| cs, cs-aot | `System.Text.Unicode.Utf8.IsValid` | `System.Text.Rune.DecodeFromUtf8` |
| c | no std UTF-8 in C | |

Checksums: quick `3000 15` `58233873360`, publish `30000 400` `15529034553600`. Overflow class `wraps`.
