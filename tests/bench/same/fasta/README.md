# fasta

The Benchmarks Game's `fasta` generator (repeat Alu sequence, then two random DNA sequences drawn from the IUB and Homo sapiens probability tables by the standard LCG), formatted in 60-byte lines and folded into a checksum instead of written to stdout so the benchmark measures code generation rather than pipe I/O. Overflow class `never`.

```
IM = 139968, IA = 3877, IC = 29573, seed = 42
LINE = 60

next_rand(&seed):
    seed = (seed * IA + IC) % IM
    return f64(seed) / f64(IM)

repeat_fasta(alu, count):
    emit count bytes cycling through alu in lines of at most 60 bytes + '\n'
    for each line buf[0..m] (including '\n'):
        for k in 0..m: total += i64(buf[k]) * (k + 1)

random_fasta(table, count, &seed):
    emit count bytes in lines of at most 60 bytes + '\n':
        for each byte:
            r = next_rand(&seed)
            j = 0
            while table[j].p < r: j += 1   (counting build increments probes per check)
            buf[k] = table[j].ch
        for k in 0..m: total += i64(buf[k]) * (k + 1)

fasta(n):
    total = 0
    total += repeat_fasta(ALU, 2 * n)
    total += random_fasta(IUB, 3 * n, &seed)
    total += random_fasta(HOMOSAPIENS, 5 * n, &seed)
    print total
```

Signed 64-bit `total` and `seed` in every column (C `int64_t`, Rust and Zig `i64`, Go and Mk `int`, C# `long`). Counting build counts `probes`, the number of cumulative-probability checks in `random_fasta`.
