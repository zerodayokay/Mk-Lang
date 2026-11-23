# app/wordfreq-app: end-to-end streaming word-frequency counter

An `app` row (tests/bench/README.md "Applications"). Unlike `idiomatic/wordfreq` (which slurps a 50 MB file into memory and repeats lookups with borrowed slices), `app/wordfreq-app` is an end-to-end streaming CLI tool: it reads a large text file sequentially in 64 KiB chunks, tokenizes ASCII words (lowercase-folded), counts frequencies in a hash map with owned keys, sorts all unique words by `(count desc, word asc)`, and prints the summary and top-K words.

## Command line

```
wordfreq-app <file> <top_k> [<max_words>]
```

Reads at most `<max_words>` words (the whole file when omitted) and prints:

```
total <total_words>
unique <unique_words>
<word_1> <count_1>
...
<word_k> <count_k>
```

where `k = min(top_k, unique_words)`.

## Input

`tests/bench/data/app-wordfreq-450mb.txt`, from `tools/bench gen` kind `words` (seed 42, bytes 450,000,000; 65,536-word skewed vocabulary of 1..12 lowercase ASCII letters separated by spaces and newlines). Sized so the fastest cell clears the 1 s floor.

## The work, every step

1. **Stream:** open `<file>` and read sequentially through a 64 KiB buffer (never holding the whole file in memory).
2. **Tokenize:** extract maximal contiguous runs of ASCII letters `[A-Za-z]`, folding `A-Z` to `a-z`, carrying partial words cleanly across 64 KiB read boundaries.
3. **Count:** increment each word's count in the language's hash map (`std` in Mk, Rust, Zig, Go, C#; hand open-addressed hash table in C), allocating/copying the key only on its first appearance.
4. **Sort:** collect all unique `(word, count)` pairs and sort by `count` descending, breaking ties by `word` ascending in bytewise (ordinal) order.
5. **Report:** write `total`, `unique`, and the top `top_k` `word count` lines to buffered stdout with LF line endings.

## The check

`"digest": true`: the checksum is the SHA-256 of the whole stdout (CR LF read as LF). Every cell must print the exact same total, unique count, and top-K list.

## Cells

| Language | Kind | Program | Libraries |
|---|---|---|---|
| Mk | `std` | `mk/wordfreq-app.mk`: `fs.File.Read` 64 KiB chunks, `Map[str, i64].New` (fast fixed hash, arena-owned keys), `List.Sort` | std only |
| C | `hand` | `wordfreq-app.c`: `fread` 64 KiB chunks, hand open-addressed linear-probing hash table (FNV-1a 64, 70% load, arena string slab, 105 lines) and `qsort` | none |
| Rust | `std` | `wordfreq-app.rs`: `File::read` 64 KiB chunks, `HashMap<String, i64>` (SipHash-1-3, `get_mut(&str)` on hits), `sort_unstable_by` | std only |
| Zig | `std` | `wordfreq-app.zig`: `File.readerStreaming` 64 KiB chunks, `std.StringHashMap(i64)` (Wyhash, arena-owned keys), `std.mem.sort` | std only |
| Go | `std` | `testdata/wordfreq-app.go`: `File.Read` 64 KiB chunks, `map[string]int64` (runtime-seeded hash), `slices.SortFunc` | std only |
| C# | `std` | `wordfreq-app.cs`: `FileStream.Read` 64 KiB chunks, `Dictionary<string, Counter>` with `GetAlternateLookup<ReadOnlySpan<char>>` (Marvin32 randomized hash, zero-alloc hits), `List.Sort` | std only |

- **Overflow class:** `never`.
- **Environment:** `MK_CARRIERS=1` (single-threaded); `publish`'s one-CPU affinity.
- **Row naming:** named `wordfreq-app` so it stays distinct from `idiomatic/wordfreq`.
