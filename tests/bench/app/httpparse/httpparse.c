// httpparse, C, std: the task of README.md, a strict RFC 9112 HTTP/1.1 request
// parser over a splitmix64-generated batch of valid and malformed requests
// (GET/POST/PUT/DELETE, paths and query strings, headers, fixed Content-Length
// and chunked bodies, plus 10 malformed classes). Uses memchr for line and
// delimiter scans, flat buffers with zero per-request heap allocation, and
// wrapping uint64_t FNV-1a digest. Standard C17 (no inline hints, no restrict,
// no attributes, no intrinsics).
// Usage: httpparse <num_requests> <rounds> <seed>.
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define FNV_BASIS 0xcbf29ce484222325ULL
#define FNV_PRIME 0x100000001b3ULL
#define MAX_HEADERS 64

typedef struct {
    uint64_t s;
} Rng;

static uint64_t rng_next(Rng *g) {
    g->s += 0x9e3779b97f4a7c15ULL;
    uint64_t z = g->s;
    z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9ULL;
    z = (z ^ (z >> 27)) * 0x94d049bb133111ebULL;
    return z ^ (z >> 31);
}

typedef struct {
    uint8_t *data;
    size_t len;
    size_t cap;
} ByteVec;

static void *xrealloc(void *p, size_t n) {
    void *q = realloc(p, n);
    if (!q && n > 0) {
        fprintf(stderr, "httpparse: out of memory\n");
        exit(1);
    }
    return q;
}

static void bv_reserve(ByteVec *v, size_t add) {
    size_t need = v->len + add;
    if (need > v->cap) {
        size_t cap = v->cap ? v->cap : 4096;
        while (cap < need) cap *= 2;
        v->data = (uint8_t *)xrealloc(v->data, cap);
        v->cap = cap;
    }
}

static void bv_push(ByteVec *v, uint8_t b) {
    bv_reserve(v, 1);
    v->data[v->len++] = b;
}

static void bv_append(ByteVec *v, const void *p, size_t n) {
    if (n == 0) return;
    bv_reserve(v, n);
    memcpy(v->data + v->len, p, n);
    v->len += n;
}

static void bv_str(ByteVec *v, const char *s) {
    bv_append(v, s, strlen(s));
}

static void bv_u64(ByteVec *v, uint64_t x) {
    char tmp[24];
    int n = 0;
    if (x == 0) {
        bv_push(v, '0');
        return;
    }
    while (x > 0) {
        tmp[n++] = (char)('0' + (x % 10));
        x /= 10;
    }
    for (int i = n - 1; i >= 0; i--) {
        bv_push(v, (uint8_t)tmp[i]);
    }
}

static void bv_hex(ByteVec *v, size_t x) {
    static const char hex[] = "0123456789abcdef";
    char tmp[16];
    int n = 0;
    if (x == 0) {
        bv_push(v, '0');
        return;
    }
    while (x > 0) {
        tmp[n++] = hex[x & 15];
        x >>= 4;
    }
    for (int i = n - 1; i >= 0; i--) {
        bv_push(v, (uint8_t)tmp[i]);
    }
}

static void build_batch(size_t n, uint64_t seed, ByteVec *data, size_t *starts) {
    Rng rng = { seed };
    for (size_t i = 0; i < n; i++) {
        starts[i] = data->len;
        uint64_t r = rng_next(&rng);
        if (r % 20 == 0) {
            switch ((r >> 8) % 10) {
            case 0: bv_str(data, "GET /api/v1/bad HTTP/2.0\r\nHost: api.example.com\r\n\r\n"); break;
            case 1: bv_str(data, "GET /api/v1/bad HTTP/1.1\nHost: api.example.com\r\n\r\n"); break;
            case 2: bv_str(data, "GET /api/v1/bad HTTP/1.1\r\nHost : api.example.com\r\n\r\n"); break;
            case 3: bv_str(data, "GET /api/v1/bad HTTP/1.1\r\nUser-Agent: bench/1.0\r\n\r\n"); break;
            case 4: bv_str(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n0\r\n\r\n"); break;
            case 5: bv_str(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\nabcd"); break;
            case 6: bv_str(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 12abc\r\n\r\nabcd"); break;
            case 7: bv_str(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 20\r\n\r\nshort"); break;
            case 8: bv_str(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\nabcd\r\n0\r\n\r\n"); break;
            default: bv_str(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n"); break;
            }
            continue;
        }
        uint64_t m = (r >> 5) % 4;
        if (m == 0) bv_str(data, "GET ");
        else if (m == 1) bv_str(data, "POST ");
        else if (m == 2) bv_str(data, "PUT ");
        else bv_str(data, "DELETE ");
        bv_str(data, "/api/v1/users/");
        bv_u64(data, (r >> 8) % 10000);
        bv_str(data, "/items/");
        bv_u64(data, (r >> 22) % 100000);
        if ((r >> 40) & 1) {
            bv_str(data, "?page=");
            bv_u64(data, (r >> 41) % 100);
            bv_str(data, "&limit=");
            bv_u64(data, 10 + ((r >> 48) % 90));
            bv_str(data, "&sort=asc");
        }
        bv_str(data, " HTTP/1.1\r\nHost: api");
        bv_u64(data, r & 7);
        bv_str(data, ".example.com\r\nUser-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) BenchAgent/1.0\r\nAccept: application/json, text/plain;q=0.9, */*;q=0.8\r\nAccept-Encoding: gzip, deflate, br\r\n");
        if ((r >> 39) & 1) bv_str(data, "Connection: close\r\n");
        else bv_str(data, "Connection: keep-alive\r\n");
        uint64_t r2 = rng_next(&rng);
        bv_str(data, "X-Request-Id: req-");
        bv_u64(data, r2 % 100000000);
        bv_str(data, "\r\n");
        if (m == 1 || m == 2) {
            bv_str(data, "Content-Type: application/json\r\n");
            if ((r2 >> 30) & 1) {
                bv_str(data, "Transfer-Encoding: chunked\r\n\r\n");
                size_t nchunks = 2 + (size_t)((r2 >> 32) % 4);
                for (size_t ci = 0; ci < nchunks; ci++) {
                    unsigned shift = (unsigned)(ci * 7);
                    size_t clen = 16 + (size_t)(((r2 >> shift) ^ ((uint64_t)ci * 13ULL)) % 64);
                    bv_hex(data, clen);
                    bv_str(data, "\r\n");
                    for (size_t j = 0; j < clen; j++) {
                        bv_push(data, (uint8_t)('a' + ((ci * 7 + j) % 26)));
                    }
                    bv_str(data, "\r\n");
                }
                bv_str(data, "0\r\n\r\n");
            } else {
                size_t blen = 32 + (size_t)((r2 >> 32) % 224);
                bv_str(data, "Content-Length: ");
                bv_u64(data, blen);
                bv_str(data, "\r\n\r\n");
                size_t off = (size_t)(r2 % 26);
                for (size_t j = 0; j < blen; j++) {
                    bv_push(data, (uint8_t)('a' + ((j + off) % 26)));
                }
            }
        } else {
            bv_str(data, "\r\n");
        }
    }
    starts[n] = data->len;
}

typedef struct {
    size_t off;
    size_t len;
} SliceSpan;

typedef struct {
    SliceSpan name;
    SliceSpan value;
} HeaderSpan;

typedef struct {
    SliceSpan method;
    SliceSpan path;
    SliceSpan query;
    HeaderSpan hdrs[MAX_HEADERS];
    size_t nhdrs;
    size_t body_off;
    size_t body_len;
    bool body_from_buf;
    bool chunked;
    bool keep_alive;
} Parsed;

static bool is_tchar(uint8_t b) {
    if ((b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')) {
        return true;
    }
    switch (b) {
    case '!': case '#': case '$': case '%': case '&': case '\'': case '*':
    case '+': case '-': case '.': case '^': case '_': case '`': case '|': case '~':
        return true;
    default:
        return false;
    }
}

static uint8_t lower_ascii(uint8_t b) {
    return (b >= 'A' && b <= 'Z') ? (uint8_t)(b + 32) : b;
}

static bool eq_ignore_ascii(const uint8_t *a, size_t n, const char *lit) {
    for (size_t i = 0; i < n; i++) {
        if (lower_ascii(a[i]) != (uint8_t)lit[i]) return false;
    }
    return true;
}

static int hex_val(uint8_t b) {
    if (b >= '0' && b <= '9') return (int)(b - '0');
    if (b >= 'a' && b <= 'f') return (int)(b - 'a' + 10);
    if (b >= 'A' && b <= 'F') return (int)(b - 'A' + 10);
    return -1;
}

static bool parse_request(const uint8_t *buf, size_t n, ByteVec *body_buf, Parsed *out) {
    const uint8_t *nl_p = (const uint8_t *)memchr(buf, '\n', n);
    if (!nl_p || nl_p == buf || nl_p[-1] != '\r') return false;
    size_t nl = (size_t)(nl_p - buf);
    size_t line_end = nl - 1;

    const uint8_t *sp1_p = (const uint8_t *)memchr(buf, ' ', line_end);
    if (!sp1_p || sp1_p == buf) return false;
    size_t sp1 = (size_t)(sp1_p - buf);
    for (size_t i = 0; i < sp1; i++) {
        if (!is_tchar(buf[i])) return false;
    }

    size_t rest1 = sp1 + 1;
    const uint8_t *sp2_p = (const uint8_t *)memchr(buf + rest1, ' ', line_end - rest1);
    if (!sp2_p || sp2_p == buf + rest1) return false;
    size_t sp2 = (size_t)(sp2_p - buf);
    if (buf[rest1] != '/') return false;

    size_t qpos = (size_t)-1;
    for (size_t i = rest1; i < sp2; i++) {
        uint8_t c = buf[i];
        if (c < 0x21 || c > 0x7e) return false;
        if (c == '?' && qpos == (size_t)-1) qpos = i;
    }

    size_t ver_start = sp2 + 1;
    if (line_end - ver_start != 8 || memcmp(buf + ver_start, "HTTP/1.1", 8) != 0) {
        return false;
    }

    out->method.off = 0;
    out->method.len = sp1;
    if (qpos != (size_t)-1) {
        out->path.off = rest1;
        out->path.len = qpos - rest1;
        out->query.off = qpos + 1;
        out->query.len = sp2 - (qpos + 1);
    } else {
        out->path.off = rest1;
        out->path.len = sp2 - rest1;
        out->query.off = 0;
        out->query.len = 0;
    }

    size_t pos = nl + 1;
    size_t nhdrs = 0;
    bool has_host = false;
    bool has_cl = false;
    size_t cl_val = 0;
    bool chunked = false;
    bool keep_alive = true;
    bool head_done = false;

    while (pos < n) {
        const uint8_t *p = (const uint8_t *)memchr(buf + pos, '\n', n - pos);
        if (!p || p == buf + pos || p[-1] != '\r') return false;
        size_t ln_end = (size_t)(p - buf) - 1;
        if (ln_end == pos) {
            pos = (size_t)(p - buf) + 1;
            head_done = true;
            break;
        }
        if (buf[pos] == ' ' || buf[pos] == '\t') return false;
        const uint8_t *col = (const uint8_t *)memchr(buf + pos, ':', ln_end - pos);
        if (!col || col == buf + pos) return false;
        size_t name_start = pos;
        size_t name_len = (size_t)(col - (buf + pos));
        for (size_t i = 0; i < name_len; i++) {
            if (!is_tchar(buf[name_start + i])) return false;
        }
        size_t vs = name_start + name_len + 1;
        size_t ve = ln_end;
        while (vs < ve && (buf[vs] == ' ' || buf[vs] == '\t')) vs++;
        while (ve > vs && (buf[ve - 1] == ' ' || buf[ve - 1] == '\t')) ve--;
        for (size_t i = vs; i < ve; i++) {
            uint8_t c = buf[i];
            if ((c < 0x20 && c != '\t') || c == 0x7f) return false;
        }
        if (nhdrs >= MAX_HEADERS) return false;
        out->hdrs[nhdrs].name.off = name_start;
        out->hdrs[nhdrs].name.len = name_len;
        out->hdrs[nhdrs].value.off = vs;
        out->hdrs[nhdrs].value.len = ve - vs;
        nhdrs++;

        if (name_len == 4 && eq_ignore_ascii(buf + name_start, 4, "host")) {
            if (has_host || ve == vs) return false;
            has_host = true;
        } else if (name_len == 14 && eq_ignore_ascii(buf + name_start, 14, "content-length")) {
            if (ve == vs) return false;
            size_t v = 0;
            for (size_t i = vs; i < ve; i++) {
                uint8_t c = buf[i];
                if (c < '0' || c > '9') return false;
                v = v * 10 + (size_t)(c - '0');
                if (v > 1073741824ULL) return false;
            }
            if (has_cl && v != cl_val) return false;
            has_cl = true;
            cl_val = v;
        } else if (name_len == 17 && eq_ignore_ascii(buf + name_start, 17, "transfer-encoding")) {
            if (chunked || (ve - vs) != 7 || !eq_ignore_ascii(buf + vs, 7, "chunked")) return false;
            chunked = true;
        } else if (name_len == 10 && eq_ignore_ascii(buf + name_start, 10, "connection")) {
            if ((ve - vs) == 5 && eq_ignore_ascii(buf + vs, 5, "close")) {
                keep_alive = false;
            } else if ((ve - vs) == 10 && eq_ignore_ascii(buf + vs, 10, "keep-alive")) {
                keep_alive = true;
            }
        }
        pos = (size_t)(p - buf) + 1;
    }

    if (!head_done || !has_host || (has_cl && chunked)) return false;
    out->nhdrs = nhdrs;
    out->chunked = chunked;
    out->keep_alive = keep_alive;

    if (chunked) {
        body_buf->len = 0;
        for (;;) {
            if (pos >= n) return false;
            const uint8_t *p = (const uint8_t *)memchr(buf + pos, '\n', n - pos);
            if (!p || p == buf + pos || p[-1] != '\r') return false;
            size_t sz_end = (size_t)(p - buf) - 1;
            size_t sz = 0;
            size_t digits = 0;
            for (size_t k = pos; k < sz_end; k++) {
                uint8_t c = buf[k];
                if (c == ';') break;
                int hv = hex_val(c);
                if (hv < 0) return false;
                sz = sz * 16 + (size_t)hv;
                if (sz > 16777216ULL) return false;
                digits++;
            }
            if (digits == 0) return false;
            pos = (size_t)(p - buf) + 1;
            if (sz == 0) {
                if (pos + 2 != n || buf[pos] != '\r' || buf[pos + 1] != '\n') return false;
                break;
            }
            if (pos + sz + 2 > n) return false;
            bv_append(body_buf, buf + pos, sz);
            pos += sz;
            if (buf[pos] != '\r' || buf[pos + 1] != '\n') return false;
            pos += 2;
        }
        out->body_off = 0;
        out->body_len = body_buf->len;
        out->body_from_buf = true;
    } else if (has_cl) {
        if (n - pos != cl_val) return false;
        out->body_off = pos;
        out->body_len = cl_val;
        out->body_from_buf = false;
    } else {
        if (pos != n) return false;
        out->body_off = 0;
        out->body_len = 0;
        out->body_from_buf = false;
    }
    return true;
}

static uint64_t fnv_byte(uint64_t h, uint8_t b) {
    return (h ^ (uint64_t)b) * FNV_PRIME;
}

static uint64_t fnv_bytes(uint64_t h, const uint8_t *p, size_t n) {
    for (size_t i = 0; i < n; i++) {
        h = (h ^ (uint64_t)p[i]) * FNV_PRIME;
    }
    return h;
}

static uint64_t fnv_bytes_lower(uint64_t h, const uint8_t *p, size_t n) {
    for (size_t i = 0; i < n; i++) {
        h = (h ^ (uint64_t)lower_ascii(p[i])) * FNV_PRIME;
    }
    return h;
}

int main(int argc, char **argv) {
    if (argc != 4) {
        fprintf(stderr, "usage: httpparse <num_requests> <rounds> <seed>\n");
        return 2;
    }
    size_t num_req = (size_t)strtoull(argv[1], NULL, 10);
    size_t rounds = (size_t)strtoull(argv[2], NULL, 10);
    uint64_t seed = (uint64_t)strtoull(argv[3], NULL, 10);

    ByteVec data = {0};
    size_t *starts = (size_t *)xrealloc(NULL, (num_req + 1) * sizeof(size_t));
    build_batch(num_req, seed, &data, starts);

    ByteVec body_buf = {0};
    Parsed parsed;

    int64_t ok_cnt = 0, err_cnt = 0, hdr_cnt = 0, body_cnt = 0, chunked_cnt = 0, ka_cnt = 0;
    uint64_t h = FNV_BASIS;

    for (size_t round = 0; round < rounds; round++) {
        for (size_t i = 0; i < num_req; i++) {
            const uint8_t *req_buf = data.data + starts[i];
            size_t req_len = starts[i + 1] - starts[i];
            if (parse_request(req_buf, req_len, &body_buf, &parsed)) {
                ok_cnt++;
                hdr_cnt += (int64_t)parsed.nhdrs;
                body_cnt += (int64_t)parsed.body_len;
                if (parsed.chunked) chunked_cnt++;
                if (parsed.keep_alive) ka_cnt++;
                h = fnv_bytes(h, req_buf + parsed.method.off, parsed.method.len);
                h = fnv_byte(h, 1);
                h = fnv_bytes(h, req_buf + parsed.path.off, parsed.path.len);
                h = fnv_byte(h, 2);
                h = fnv_bytes(h, req_buf + parsed.query.off, parsed.query.len);
                h = fnv_byte(h, 3);
                for (size_t k = 0; k < parsed.nhdrs; k++) {
                    HeaderSpan hs = parsed.hdrs[k];
                    h = fnv_bytes_lower(h, req_buf + hs.name.off, hs.name.len);
                    h = fnv_byte(h, 4);
                    h = fnv_bytes(h, req_buf + hs.value.off, hs.value.len);
                    h = fnv_byte(h, 5);
                }
                if (parsed.body_from_buf) {
                    h = fnv_bytes(h, body_buf.data, body_buf.len);
                } else {
                    h = fnv_bytes(h, req_buf + parsed.body_off, parsed.body_len);
                }
                h = fnv_byte(h, 6);
            } else {
                err_cnt++;
                h = fnv_byte(h, 0xff);
                h = fnv_byte(h, (uint8_t)(i % 251));
            }
        }
    }

    printf("ok=%lld err=%lld hdrs=%lld body=%lld chunked=%lld keepalive=%lld fnv=%016llx\n",
           (long long)ok_cnt, (long long)err_cnt, (long long)hdr_cnt, (long long)body_cnt,
           (long long)chunked_cnt, (long long)ka_cnt, (unsigned long long)h);

    free(body_buf.data);
    free(starts);
    free(data.data);
    return 0;
}
