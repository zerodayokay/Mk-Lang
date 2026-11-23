// fasta, C: the reference algorithm of README.md (the Benchmarks Game's fasta
// generator, hashed by line into an int64_t checksum). Signed int64_t
// accumulators and LCG state (overflow UB, never reached). No bounds checks.
// Built by tools/bench with clang 21: -O3 -march=x86-64-v2 -ffp-contract=off
// -fms-runtime-lib=static -fuse-ld=lld.
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifndef BENCH_COUNT
#define BENCH_COUNT 0
#endif

enum {
    IM = 139968,
    IA = 3877,
    IC = 29573,
    LINE_LEN = 60,
};

struct freq {
    uint8_t ch;
    double p;
};

static void make_cumulative(struct freq *tab, int64_t len) {
    double cp = 0.0;
    for (int64_t i = 0; i < len; i++) {
        cp = cp + tab[i].p;
        tab[i].p = cp;
    }
    tab[len - 1].p = 1.0;
}

static double next_rand(int64_t *seed) {
    *seed = (*seed * IA + IC) % IM;
    return (double)(*seed) / (double)IM;
}

static int64_t line_sum(const uint8_t *buf, int64_t m) {
    int64_t s = 0;
    for (int64_t k = 0; k < m; k++) {
        s += (int64_t)buf[k] * (k + 1);
    }
    return s;
}

static int64_t repeat_fasta(const uint8_t *alu, int64_t alu_len, int64_t count) {
    uint8_t buf[61];
    int64_t pos = 0;
    int64_t rem = count;
    int64_t total = 0;
    while (rem > 0) {
        int64_t m = rem < LINE_LEN ? rem : LINE_LEN;
        for (int64_t k = 0; k < m; k++) {
            buf[k] = alu[pos];
            pos += 1;
            if (pos == alu_len) {
                pos = 0;
            }
        }
        buf[m] = 10;
        total += line_sum(buf, m + 1);
        rem -= m;
    }
    return total;
}

static int64_t random_fasta(const struct freq *tab, int64_t count, int64_t *seed, uint64_t *probes) {
    uint8_t buf[61];
    int64_t rem = count;
    int64_t total = 0;
    while (rem > 0) {
        int64_t m = rem < LINE_LEN ? rem : LINE_LEN;
        for (int64_t k = 0; k < m; k++) {
            double r = next_rand(seed);
            int64_t j = 0;
            for (;;) {
                if (BENCH_COUNT) {
                    *probes += 1;
                }
                if (tab[j].p >= r) {
                    break;
                }
                j += 1;
            }
            buf[k] = tab[j].ch;
        }
        buf[m] = 10;
        total += line_sum(buf, m + 1);
        rem -= m;
    }
    return total;
}

int main(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "usage: fasta <n>\n");
        return 2;
    }
    int64_t n = strtoll(argv[1], NULL, 10);
    static const char alu[] =
        "GGCCGGGCGCGGTGGCTCACGCCTGTAATCCCAGCACTTTGGGAGGCCGAGGCGGGCGGATCACCTGAGGTCAGGAGTTCGAGACCAGCCTGGCCAACATGGTGAAACCCCGTCTCTACTAAAAATACAAAAATTAGCCGGGCGTGGTGGCGCGCGCCTGTAATCCCAGCTACTCGGGAGGCTGAGGCAGGAGAATCGCTTGAACCCGGGAGGCGGAGGTTGCAGTGAGCCGAGATCGCGCCACTGCACTCCAGCCTGGGCGACAGAGCGAGACTCCGTCTCAAAAA";
    struct freq iub[15] = {
        {'a', 0.27}, {'c', 0.12}, {'g', 0.12}, {'t', 0.27},
        {'B', 0.02}, {'D', 0.02}, {'H', 0.02}, {'K', 0.02},
        {'M', 0.02}, {'N', 0.02}, {'R', 0.02}, {'S', 0.02},
        {'V', 0.02}, {'W', 0.02}, {'Y', 0.02},
    };
    struct freq homo[4] = {
        {'a', 0.3029549426680},
        {'c', 0.1979883004921},
        {'g', 0.1975473066391},
        {'t', 0.3015094502008},
    };
    make_cumulative(iub, 15);
    make_cumulative(homo, 4);
    int64_t seed = 42;
    uint64_t probes = 0;
    int64_t total = 0;
    total += repeat_fasta((const uint8_t *)alu, (int64_t)(sizeof(alu) - 1), 2 * n);
    total += random_fasta(iub, 3 * n, &seed, &probes);
    total += random_fasta(homo, 5 * n, &seed, &probes);
    printf("%lld\n", (long long)total);
    if (BENCH_COUNT) {
        printf("counts probes=%llu\n", (unsigned long long)probes);
    }
    return 0;
}
