// wordfreq-app, C, hand: the task of README.md. Streams the input file through
// a 64 KiB fread buffer, tokenizes ASCII words ([A-Za-z], folded to lowercase)
// carrying partial words across chunk boundaries, counts frequencies in a hand
// open-addressed linear-probing hash table (FNV-1a 64 hash, 70% max load,
// arena-allocated string slab, 105 lines), sorts unique entries with qsort by
// (count desc, word asc), and prints total, unique and top-K word counts.
// Standard C17 (no inline hints, no restrict, no attributes).
// Usage: wordfreq-app <file> <top_k> [<max_words>].
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CHUNK_SIZE 65536
#define FNV_BASIS 0xcbf29ce484222325ULL
#define FNV_PRIME 0x100000001b3ULL

static void *xmalloc(size_t n) {
    void *p = malloc(n);
    if (!p && n > 0) {
        fprintf(stderr, "wordfreq-app: out of memory\n");
        exit(1);
    }
    return p;
}

static void *xcalloc(size_t count, size_t size) {
    void *p = calloc(count, size);
    if (!p && count > 0 && size > 0) {
        fprintf(stderr, "wordfreq-app: out of memory\n");
        exit(1);
    }
    return p;
}

static void *xrealloc(void *p, size_t n) {
    void *q = realloc(p, n);
    if (!q && n > 0) {
        fprintf(stderr, "wordfreq-app: out of memory\n");
        exit(1);
    }
    return q;
}

// ---- hand open-addressed hash table + string slab (105 lines)

typedef struct Slab {
    struct Slab *next;
    size_t used;
    size_t cap;
    char data[];
} Slab;

typedef struct {
    char *key;
    uint32_t len;
    uint64_t hash;
    int64_t count;
} Slot;

typedef struct {
    Slot *slots;
    size_t cap;
    size_t len;
    Slab *slab;
} Table;

static char *table_dup(Table *t, const char *s, size_t len) {
    size_t need = len + 1;
    if (!t->slab || t->slab->used + need > t->slab->cap) {
        size_t cap = need > (256u << 10) ? need : (256u << 10);
        Slab *ns = (Slab *)xmalloc(sizeof(Slab) + cap);
        ns->next = t->slab;
        ns->used = 0;
        ns->cap = cap;
        t->slab = ns;
    }
    char *dst = t->slab->data + t->slab->used;
    memcpy(dst, s, len);
    dst[len] = '\0';
    t->slab->used += need;
    return dst;
}

static uint64_t fnv64(const char *s, size_t len) {
    uint64_t h = FNV_BASIS;
    for (size_t i = 0; i < len; i++) {
        h = (h ^ (uint8_t)s[i]) * FNV_PRIME;
    }
    return h;
}

static void table_init(Table *t, size_t init_cap) {
    t->slots = (Slot *)xcalloc(init_cap, sizeof(Slot));
    t->cap = init_cap;
    t->len = 0;
    t->slab = NULL;
}

static void table_grow(Table *t) {
    size_t ncap = t->cap * 2;
    Slot *ns = (Slot *)xcalloc(ncap, sizeof(Slot));
    size_t mask = ncap - 1;
    for (size_t i = 0; i < t->cap; i++) {
        if (t->slots[i].key) {
            size_t idx = (size_t)t->slots[i].hash & mask;
            while (ns[idx].key) {
                idx = (idx + 1) & mask;
            }
            ns[idx] = t->slots[i];
        }
    }
    free(t->slots);
    t->slots = ns;
    t->cap = ncap;
}

static void table_inc(Table *t, const char *s, size_t len) {
    if ((t->len + 1) * 10 >= t->cap * 7) {
        table_grow(t);
    }
    uint64_t h = fnv64(s, len);
    size_t mask = t->cap - 1;
    size_t idx = (size_t)h & mask;
    for (;;) {
        Slot *sl = &t->slots[idx];
        if (!sl->key) {
            sl->key = table_dup(t, s, len);
            sl->len = (uint32_t)len;
            sl->hash = h;
            sl->count = 1;
            t->len++;
            return;
        }
        if (sl->hash == h && sl->len == len && memcmp(sl->key, s, len) == 0) {
            sl->count++;
            return;
        }
        idx = (idx + 1) & mask;
    }
}

static void table_free(Table *t) {
    free(t->slots);
    Slab *s = t->slab;
    while (s) {
        Slab *nxt = s->next;
        free(s);
        s = nxt;
    }
}

// ---- sorting and driver

typedef struct {
    const char *word;
    int64_t count;
} Entry;

static int cmp_entry(const void *pa, const void *pb) {
    const Entry *a = (const Entry *)pa;
    const Entry *b = (const Entry *)pb;
    if (a->count != b->count) {
        return a->count < b->count ? 1 : -1;
    }
    return strcmp(a->word, b->word);
}

int main(int argc, char **argv) {
    if (argc < 3 || argc > 4) {
        fprintf(stderr, "usage: wordfreq-app <file> <top_k> [<max_words>]\n");
        return 2;
    }
    int64_t top_k = strtoll(argv[2], NULL, 10);
    int64_t limit = argc == 4 ? strtoll(argv[3], NULL, 10) : -1;

    FILE *f = fopen(argv[1], "rb");
    if (!f) {
        fprintf(stderr, "wordfreq-app: cannot open %s\n", argv[1]);
        return 1;
    }

    uint8_t *buf = (uint8_t *)xmalloc(CHUNK_SIZE);
    size_t wcap = 256;
    char *wbuf = (char *)xmalloc(wcap);
    size_t wlen = 0;

    Table tab;
    table_init(&tab, 1024);

    int64_t total = 0;
    int done = 0;
    while (!done) {
        size_t n = fread(buf, 1, CHUNK_SIZE, f);
        if (n == 0) break;
        size_t i = 0;
        while (i < n) {
            uint8_t c = buf[i];
            if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
                if (wlen == 0) {
                    size_t start = i;
                    int has_upper = (c >= 'A' && c <= 'Z');
                    i++;
                    while (i < n) {
                        uint8_t b = buf[i];
                        if (b >= 'a' && b <= 'z') {
                            i++;
                        } else if (b >= 'A' && b <= 'Z') {
                            has_upper = 1;
                            i++;
                        } else {
                            break;
                        }
                    }
                    size_t len = i - start;
                    if (i == n) {
                        if (len > wcap) {
                            while (wcap < len) wcap *= 2;
                            wbuf = (char *)xrealloc(wbuf, wcap);
                        }
                        for (size_t j = 0; j < len; j++) {
                            uint8_t ch = buf[start + j];
                            wbuf[j] = (char)(ch >= 'A' && ch <= 'Z' ? ch + 32 : ch);
                        }
                        wlen = len;
                    } else if (!has_upper) {
                        table_inc(&tab, (const char *)(buf + start), len);
                        total++;
                        if (limit >= 0 && total >= limit) { done = 1; break; }
                    } else {
                        if (len > wcap) {
                            while (wcap < len) wcap *= 2;
                            wbuf = (char *)xrealloc(wbuf, wcap);
                        }
                        for (size_t j = 0; j < len; j++) {
                            uint8_t ch = buf[start + j];
                            wbuf[j] = (char)(ch >= 'A' && ch <= 'Z' ? ch + 32 : ch);
                        }
                        table_inc(&tab, wbuf, len);
                        total++;
                        if (limit >= 0 && total >= limit) { done = 1; break; }
                    }
                } else {
                    if (wlen == wcap) {
                        wcap *= 2;
                        wbuf = (char *)xrealloc(wbuf, wcap);
                    }
                    wbuf[wlen++] = (char)(c >= 'A' && c <= 'Z' ? c + 32 : c);
                    i++;
                }
            } else {
                if (wlen > 0) {
                    table_inc(&tab, wbuf, wlen);
                    wlen = 0;
                    total++;
                    if (limit >= 0 && total >= limit) { done = 1; break; }
                }
                i++;
            }
        }
    }
    if (!done && wlen > 0) {
        table_inc(&tab, wbuf, wlen);
        total++;
    }
    fclose(f);

    Entry *entries = (Entry *)xmalloc(tab.len * sizeof(Entry));
    size_t ei = 0;
    for (size_t i = 0; i < tab.cap; i++) {
        if (tab.slots[i].key) {
            entries[ei].word = tab.slots[i].key;
            entries[ei].count = tab.slots[i].count;
            ei++;
        }
    }
    qsort(entries, ei, sizeof(Entry), cmp_entry);

    size_t k = top_k < 0 ? 0 : (size_t)top_k;
    if (k > ei) k = ei;
    printf("total %lld\nunique %zu\n", (long long)total, ei);
    for (size_t i = 0; i < k; i++) {
        printf("%s %lld\n", entries[i].word, (long long)entries[i].count);
    }

    free(entries);
    table_free(&tab);
    free(wbuf);
    free(buf);
    return 0;
}
