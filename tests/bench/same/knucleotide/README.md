# knucleotide

The Benchmarks Game's `k-nucleotide` workload with one fixed open-addressed hash table specification in every language: generate a DNA sequence of length `n` using the Homo sapiens LCG (2-bit codes `0..3` for `A, C, G, T`), then for `k` in `[1, 2, 3, 4, 6, 12, 18]` count every `k`-mer in a power-of-2 linear-probing table of `(key: u64, val: u64)` entries (`val == 0` empty, initial capacity 64, Fibonacci hash `(key * 0x9e3779b97f4a7c15) >> (64 - shift)`, double capacity before insert when `(len + 1) * 10 > cap * 7`), and fold unique counts, max frequency and the counts of the classic Benchmarks Game target k-mers into a wrapping `u64` checksum. Overflow class `wraps`.

```
knucleotide(n, rounds):
    seq = n bytes in {0, 1, 2, 3} from Homo sapiens LCG (seed = 42, IM = 139968, IA = 3877, IC = 29573)
    total: u64 = 0
    repeat rounds times:
        for k in [1, 2, 3, 4, 6, 12, 18]:
            tab = new Table(cap = 64, shift = 6)
            mask = (1 << (2 * k)) - 1
            key = pack first k - 1 codes
            for i in (k - 1)..n:
                key = ((key << 2) | u64(seq[i])) & mask
                tab.inc(key)
            total = total * 31 + u64(tab.len)
            total = total * 31 + max(e.val for e in tab.entries)
            for each target k-mer of length k:
                total = total * 31 + tab.get(target)
    print total
```

Unsigned 64-bit `total`, `key`, and `val` in every column (wrapping arithmetic). Counting build counts `probes` (every slot inspection in `inc` and `get`) and `rehashes` (every table growth). In Mk, each `CountK` call uses a fresh `Arena` freed on return (each grow allocates a fresh zeroed slice, matching C `calloc`, Rust `vec!`, Zig `alloc`+`@memset`, Go `make`, C# `new[]`; retaining the previous power-of-2 slice until `CountK` returns costs more memory and gives Mk no speed advantage).
