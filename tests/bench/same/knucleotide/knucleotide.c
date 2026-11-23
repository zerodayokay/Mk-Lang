// knucleotide, C: the reference algorithm of README.md (2-bit packed DNA
// k-mers in a fixed open-addressed linear-probing hash table, Fibonacci hash,
// 70% load factor). Unsigned uint64_t hashing and checksum (overflow class
// "wraps"). No bounds checks. Built by tools/bench with clang 21: -O3
// -march=x86-64-v2 -ffp-contract=off -fms-runtime-lib=static -fuse-ld=lld.
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#ifndef BENCH_COUNT
#define BENCH_COUNT 0
#endif

enum {
    IM = 139968,
    IA = 3877,
    IC = 29573,
};

static const uint64_t FIB_MUL = 0x9e3779b97f4a7c15ULL;

struct entry {
    uint64_t key;
    uint64_t val;
};

struct table {
    struct entry *entries;
    int64_t cap;
    uint32_t shift;
    int64_t len;
};

static void table_init(struct table *tab) {
    tab->entries = calloc(64, sizeof(struct entry));
    if (tab->entries == NULL) {
        exit(1);
    }
    tab->cap = 64;
    tab->shift = 6;
    tab->len = 0;
}

static void table_grow(struct table *tab, uint64_t *rehashes) {
    if (BENCH_COUNT) {
        *rehashes += 1;
    }
    int64_t new_cap = tab->cap * 2;
    uint32_t new_shift = tab->shift + 1;
    struct entry *next = calloc((size_t)new_cap, sizeof(struct entry));
    if (next == NULL) {
        exit(1);
    }
    int64_t mask = new_cap - 1;
    uint32_t sh = 64 - new_shift;
    for (int64_t i = 0; i < tab->cap; i++) {
        struct entry e = tab->entries[i];
        if (e.val != 0) {
            int64_t slot = (int64_t)((e.key * FIB_MUL) >> sh) & mask;
            for (;;) {
                if (next[slot].val == 0) {
                    next[slot] = e;
                    break;
                }
                slot = (slot + 1) & mask;
            }
        }
    }
    free(tab->entries);
    tab->entries = next;
    tab->cap = new_cap;
    tab->shift = new_shift;
}

static void table_inc(struct table *tab, uint64_t key, uint64_t *probes, uint64_t *rehashes) {
    if ((tab->len + 1) * 10 > tab->cap * 7) {
        table_grow(tab, rehashes);
    }
    int64_t mask = tab->cap - 1;
    int64_t slot = (int64_t)((key * FIB_MUL) >> (64 - tab->shift)) & mask;
    for (;;) {
        if (BENCH_COUNT) {
            *probes += 1;
        }
        uint64_t v = tab->entries[slot].val;
        if (v == 0) {
            tab->entries[slot].key = key;
            tab->entries[slot].val = 1;
            tab->len += 1;
            return;
        }
        if (tab->entries[slot].key == key) {
            tab->entries[slot].val = v + 1;
            return;
        }
        slot = (slot + 1) & mask;
    }
}

static uint64_t table_get(const struct table *tab, uint64_t key, uint64_t *probes) {
    int64_t mask = tab->cap - 1;
    int64_t slot = (int64_t)((key * FIB_MUL) >> (64 - tab->shift)) & mask;
    for (;;) {
        if (BENCH_COUNT) {
            *probes += 1;
        }
        uint64_t v = tab->entries[slot].val;
        if (v == 0) {
            return 0;
        }
        if (tab->entries[slot].key == key) {
            return v;
        }
        slot = (slot + 1) & mask;
    }
}

static uint64_t pack_seq(const char *s) {
    uint64_t key = 0;
    for (int64_t i = 0; s[i] != 0; i++) {
        uint64_t code = 3;
        if (s[i] == 'A') {
            code = 0;
        } else if (s[i] == 'C') {
            code = 1;
        } else if (s[i] == 'G') {
            code = 2;
        }
        key = (key << 2) | code;
    }
    return key;
}

static uint64_t count_k(const uint8_t *seq, int64_t n, int64_t k, uint64_t *probes, uint64_t *rehashes) {
    struct table tab;
    table_init(&tab);
    if (n >= k) {
        uint64_t mask = ((uint64_t)1 << (uint32_t)(2 * k)) - 1;
        uint64_t key = 0;
        for (int64_t i = 0; i < k - 1; i++) {
            key = (key << 2) | (uint64_t)seq[i];
        }
        for (int64_t i = k - 1; i < n; i++) {
            key = ((key << 2) | (uint64_t)seq[i]) & mask;
            table_inc(&tab, key, probes, rehashes);
        }
    }
    uint64_t h = (uint64_t)tab.len;
    uint64_t max_val = 0;
    for (int64_t i = 0; i < tab.cap; i++) {
        if (tab.entries[i].val > max_val) {
            max_val = tab.entries[i].val;
        }
    }
    h = h * 31 + max_val;
    if (k == 1) {
        for (uint64_t c = 0; c < 4; c++) {
            h = h * 31 + table_get(&tab, c, probes);
        }
    } else if (k == 2) {
        for (uint64_t c = 0; c < 16; c++) {
            h = h * 31 + table_get(&tab, c, probes);
        }
    } else if (k == 3) {
        h = h * 31 + table_get(&tab, pack_seq("GGT"), probes);
    } else if (k == 4) {
        h = h * 31 + table_get(&tab, pack_seq("GGTA"), probes);
    } else if (k == 6) {
        h = h * 31 + table_get(&tab, pack_seq("GGTATT"), probes);
    } else if (k == 12) {
        h = h * 31 + table_get(&tab, pack_seq("GGTATTTTAATT"), probes);
    } else if (k == 18) {
        h = h * 31 + table_get(&tab, pack_seq("GGTATTTTAATTTATAGT"), probes);
    }
    free(tab.entries);
    return h;
}

int main(int argc, char **argv) {
    if (argc != 3) {
        fprintf(stderr, "usage: knucleotide <n> <rounds>\n");
        return 2;
    }
    int64_t n = strtoll(argv[1], NULL, 10);
    int64_t rounds = strtoll(argv[2], NULL, 10);
    uint8_t *seq = malloc((size_t)n);
    if (seq == NULL) {
        return 1;
    }
    double p0 = 0.3029549426680;
    double p1 = p0 + 0.1979883004921;
    double p2 = p1 + 0.1975473066391;
    int64_t seed = 42;
    for (int64_t i = 0; i < n; i++) {
        seed = (seed * IA + IC) % IM;
        double r = (double)seed / (double)IM;
        if (r < p0) {
            seq[i] = 0;
        } else if (r < p1) {
            seq[i] = 1;
        } else if (r < p2) {
            seq[i] = 2;
        } else {
            seq[i] = 3;
        }
    }
    static const int64_t ks[7] = {1, 2, 3, 4, 6, 12, 18};
    uint64_t probes = 0;
    uint64_t rehashes = 0;
    uint64_t total = 0;
    for (int64_t r = 0; r < rounds; r++) {
        for (int i = 0; i < 7; i++) {
            total = total * 31 + count_k(seq, n, ks[i], &probes, &rehashes);
        }
    }
    printf("%llu\n", (unsigned long long)total);
    if (BENCH_COUNT) {
        printf("counts probes=%llu rehashes=%llu\n", (unsigned long long)probes, (unsigned long long)rehashes);
    }
    free(seq);
    return 0;
}
