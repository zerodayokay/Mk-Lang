# regex-redux (idiomatic)

**Task.** Regex-redux-style DNA matching and replacement over a generated FASTA sequence. `regex-redux <n> <rounds>`: deterministically generate a FASTA sequence of `n` bases wrapped at 60 characters per line (`>ONE` with `n/2` Homo sapiens `a/c/g/t` bases and `>TWO` with `n - n/2` IUB bases using an exact integer LCG). Compile all 15 regular expressions once, then for each round:

1. Strip FASTA headers and newlines with `>[^\n]*\n|\n` → `""`.
2. Count non-overlapping matches for the 9 classic DNA alternation/class patterns (`agggtaaa|tttaccct`, `[cgt]gggtaaa|tttaccc[acg]`, `a[act]ggtaaa|tttacc[agt]t`, `ag[act]gtaaa|tttac[agt]ct`, `agg[act]taaa|ttta[agt]cct`, `aggg[acg]aaa|ttt[cgt]ccct`, `agggt[cgt]aa|tt[acg]accct`, `agggta[cgt]a|t[acg]taccct`, `agggtaa[cgt]|[acg]ttaccct`).
3. Apply the 5 classic cascading IUB replacements sequentially (`tHa[Nt]` → `<4>`, `aND|caN|Ha[DS]|WaS` → `<3>`, `a[NSt]|BY` → `<2>`, `<[^>]*>` → `|`, `\|[^|][^|]*\|` → `-`).
4. Fold raw length, cleaned length, all 9 match counts and final length via `h = h *% 131 +% v` and sum across rounds.

**Cells.**

| Column | Regex engine |
|---|---|
| mk | `std.regex` (`Compile`, `Regex.FindAll`, `Regex.Replace`) in one scratch arena reset per round |
| go | `regexp` (`MustCompile`, `FindAllStringIndex`, `ReplaceAllString`) |
| cs, cs-aot | `System.Text.RegularExpressions.Regex` (`RegexOptions.Compiled \| RegexOptions.CultureInvariant`, `Regex.Count`, `Regex.Replace`) |
| c | no std regex in C |
| rust | no std regex in Rust |
| zig | no std regex in Zig |

Checksums: quick `50000 2` `4605541440568842582`, publish `1000000 56` `3676099772158368152`. Overflow class `wraps`.
