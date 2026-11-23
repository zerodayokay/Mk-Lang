// httpparse, Mk, std: the task of ../README.md, a strict RFC 9112 HTTP/1.1
// request parser over a splitmix64-generated batch of valid and malformed
// requests (GET/POST/PUT/DELETE, paths and query strings, headers, fixed
// Content-Length and chunked bodies, plus 10 malformed classes). Uses
// std.simd.IndexByte (allowed std routine) for line and delimiter scans and
// arena-allocated slices/lists with zero per-request heap allocation.
// Usage: httpparse <num_requests> <rounds> <seed>.

import std.simd

const FnvBasis: u64 = 0xcbf29ce484222325
const FnvPrime: u64 = 0x100000001b3
const MaxHeaders: int = 64

struct Rng {
    s: u64
}

Rng.Next(mut self): u64 {
    self.s = self.s +% 0x9e3779b97f4a7c15
    var z := self.s
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

struct SliceSpan {
    off: int
    len: int
}

struct HeaderSpan {
    name: SliceSpan
    value: SliceSpan
}

struct Parsed {
    method: SliceSpan
    path: SliceSpan
    query: SliceSpan
    nhdrs: int
    body_off: int
    body_len: int
    body_from_buf: bool
    chunked: bool
    keep_alive: bool
}

IsTchar(b: u8): bool {
    if (b >= 48 && b <= 57) || (b >= 65 && b <= 90) || (b >= 97 && b <= 122) {
        return true
    }
    match b {
        33 | 35 | 36 | 37 | 38 | 39 | 42 | 43 | 45 | 46 | 94 | 95 | 96 | 124 | 126 {
            return true
        }
        _ {
            return false
        }
    }
}

LowerAscii(b: u8): u8 {
    if b >= 65 && b <= 90 {
        return b + 32
    }
    return b
}

EqIgnoreAscii(a: []u8, lit: []u8): bool {
    if a.Len() != lit.Len() {
        return false
    }
    for i in 0..a.Len() {
        if LowerAscii(a[i]) != lit[i] {
            return false
        }
    }
    return true
}

HexVal(b: u8): int {
    if b >= 48 && b <= 57 {
        return int(b - 48)
    }
    if b >= 97 && b <= 102 {
        return int(b - 87)
    }
    if b >= 65 && b <= 70 {
        return int(b - 55)
    }
    return -1
}

PushBytes(dst: mut List[u8], s: []u8): OutOfMemory!void {
    for i in 0..s.Len() {
        try dst.Push(s[i])
    }
}

PushU64Dec(dst: mut List[u8], v: u64): OutOfMemory!void {
    if v == 0 {
        try dst.Push(48)
        return
    }
    var tmp: [24]u8 = [0; 24]
    var n := 0
    var x := v
    for x > 0 {
        tmp[n] = Cast[u8](48 + (x % 10))
        x = x / 10
        n += 1
    }
    var i := n - 1
    for i >= 0 {
        try dst.Push(tmp[i])
        i -= 1
    }
}

PushHexLower(dst: mut List[u8], v: int): OutOfMemory!void {
    hex := b"0123456789abcdef"
    if v == 0 {
        try dst.Push(48)
        return
    }
    var tmp: [16]u8 = [0; 16]
    var n := 0
    var x := v
    for x > 0 {
        tmp[n] = hex[x & 15]
        x = x >> 4
        n += 1
    }
    var i := n - 1
    for i >= 0 {
        try dst.Push(tmp[i])
        i -= 1
    }
}

BuildBatch(n: int, seed: u64, data: mut List[u8], starts: mut List[int]): OutOfMemory!void {
    var rng := Rng(s: seed)
    for _ in 0..n {
        try starts.Push(data.Len())
        r := rng.Next()
        if r % 20 == 0 {
            k := (r >> 8) % 10
            match k {
                0 { try PushBytes(data, b"GET /api/v1/bad HTTP/2.0\r\nHost: api.example.com\r\n\r\n") }
                1 { try PushBytes(data, b"GET /api/v1/bad HTTP/1.1\nHost: api.example.com\r\n\r\n") }
                2 { try PushBytes(data, b"GET /api/v1/bad HTTP/1.1\r\nHost : api.example.com\r\n\r\n") }
                3 { try PushBytes(data, b"GET /api/v1/bad HTTP/1.1\r\nUser-Agent: bench/1.0\r\n\r\n") }
                4 { try PushBytes(data, b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n0\r\n\r\n") }
                5 { try PushBytes(data, b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\nabcd") }
                6 { try PushBytes(data, b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 12abc\r\n\r\nabcd") }
                7 { try PushBytes(data, b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 20\r\n\r\nshort") }
                8 { try PushBytes(data, b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\nabcd\r\n0\r\n\r\n") }
                _ { try PushBytes(data, b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n") }
            }
            continue
        }
        m := (r >> 5) % 4
        match m {
            0 { try PushBytes(data, b"GET ") }
            1 { try PushBytes(data, b"POST ") }
            2 { try PushBytes(data, b"PUT ") }
            _ { try PushBytes(data, b"DELETE ") }
        }
        try PushBytes(data, b"/api/v1/users/")
        try PushU64Dec(data, (r >> 8) % 10000)
        try PushBytes(data, b"/items/")
        try PushU64Dec(data, (r >> 22) % 100000)
        if ((r >> 40) & 1) == 1 {
            try PushBytes(data, b"?page=")
            try PushU64Dec(data, (r >> 41) % 100)
            try PushBytes(data, b"&limit=")
            try PushU64Dec(data, 10 + ((r >> 48) % 90))
            try PushBytes(data, b"&sort=asc")
        }
        try PushBytes(data, b" HTTP/1.1\r\nHost: api")
        try PushU64Dec(data, r & 7)
        try PushBytes(data, b".example.com\r\nUser-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) BenchAgent/1.0\r\nAccept: application/json, text/plain;q=0.9, */*;q=0.8\r\nAccept-Encoding: gzip, deflate, br\r\n")
        if ((r >> 39) & 1) == 1 {
            try PushBytes(data, b"Connection: close\r\n")
        } else {
            try PushBytes(data, b"Connection: keep-alive\r\n")
        }
        r2 := rng.Next()
        try PushBytes(data, b"X-Request-Id: req-")
        try PushU64Dec(data, r2 % 100000000)
        try PushBytes(data, b"\r\n")
        if m == 1 || m == 2 {
            try PushBytes(data, b"Content-Type: application/json\r\n")
            if ((r2 >> 30) & 1) == 1 {
                try PushBytes(data, b"Transfer-Encoding: chunked\r\n\r\n")
                nchunks := 2 + Cast[int]((r2 >> 32) % 4)
                for ci in 0..nchunks {
                    shift := Cast[u64](ci * 7)
                    clen := 16 + Cast[int](((r2 >> shift) ^ (Cast[u64](ci) * 13)) % 64)
                    try PushHexLower(data, clen)
                    try PushBytes(data, b"\r\n")
                    for j in 0..clen {
                        try data.Push(Cast[u8](97 + ((ci * 7 + j) % 26)))
                    }
                    try PushBytes(data, b"\r\n")
                }
                try PushBytes(data, b"0\r\n\r\n")
            } else {
                blen := 32 + Cast[int]((r2 >> 32) % 224)
                try PushBytes(data, b"Content-Length: ")
                try PushU64Dec(data, Cast[u64](blen))
                try PushBytes(data, b"\r\n\r\n")
                off := Cast[int](r2 % 26)
                for j in 0..blen {
                    try data.Push(Cast[u8](97 + ((j + off) % 26)))
                }
            }
        } else {
            try PushBytes(data, b"\r\n")
        }
    }
    try starts.Push(data.Len())
}

ParseRequest(buf: []u8, hdrs: mut []HeaderSpan, body_buf: mut List[u8], out: mut Parsed): OutOfMemory!bool {
    n := buf.Len()
    nl := simd.IndexByte(buf, 10) else {
        return false
    }
    if nl < 1 || buf[nl - 1] != 13 {
        return false
    }
    line_end := nl - 1
    sp1 := simd.IndexByte(buf[0..line_end], 32) else {
        return false
    }
    if sp1 <= 0 {
        return false
    }
    for i in 0..sp1 {
        if !IsTchar(buf[i]) {
            return false
        }
    }
    rest1 := sp1 + 1
    sp2_rel := simd.IndexByte(buf[rest1..line_end], 32) else {
        return false
    }
    if sp2_rel <= 0 {
        return false
    }
    sp2 := rest1 + sp2_rel
    if buf[rest1] != 47 {
        return false
    }
    var qpos := -1
    for i in rest1..sp2 {
        c := buf[i]
        if c < 33 || c > 126 {
            return false
        }
        if c == 63 && qpos < 0 {
            qpos = i
        }
    }
    ver_start := sp2 + 1
    if line_end - ver_start != 8 {
        return false
    }
    http11 := b"HTTP/1.1"
    for i in 0..8 {
        if buf[ver_start + i] != http11[i] {
            return false
        }
    }

    out.method = SliceSpan(off: 0, len: sp1)
    if qpos >= 0 {
        out.path = SliceSpan(off: rest1, len: qpos - rest1)
        out.query = SliceSpan(off: qpos + 1, len: sp2 - (qpos + 1))
    } else {
        out.path = SliceSpan(off: rest1, len: sp2 - rest1)
        out.query = SliceSpan(off: 0, len: 0)
    }

    var pos := nl + 1
    var nhdrs := 0
    var has_host := false
    var has_cl := false
    var cl_val := 0
    var chunked := false
    var keep_alive := true
    var head_done := false

    for pos < n {
        ln_rel := simd.IndexByte(buf[pos..n], 10) else {
            return false
        }
        if ln_rel < 1 {
            return false
        }
        ln_end := pos + ln_rel - 1
        if buf[ln_end] != 13 {
            return false
        }
        if ln_end == pos {
            pos = pos + ln_rel + 1
            head_done = true
            break
        }
        if buf[pos] == 32 || buf[pos] == 9 {
            return false
        }
        colon_rel := simd.IndexByte(buf[pos..ln_end], 58) else {
            return false
        }
        if colon_rel <= 0 {
            return false
        }
        name_start := pos
        name_len := colon_rel
        for i in 0..name_len {
            if !IsTchar(buf[name_start + i]) {
                return false
            }
        }
        var vs := pos + colon_rel + 1
        var ve := ln_end
        for vs < ve && (buf[vs] == 32 || buf[vs] == 9) {
            vs += 1
        }
        for ve > vs && (buf[ve - 1] == 32 || buf[ve - 1] == 9) {
            ve -= 1
        }
        for i in vs..ve {
            c := buf[i]
            if (c < 32 && c != 9) || c == 127 {
                return false
            }
        }
        if nhdrs >= MaxHeaders {
            return false
        }
        hdrs[nhdrs] = HeaderSpan(name: SliceSpan(off: name_start, len: name_len), value: SliceSpan(off: vs, len: ve - vs))
        nhdrs += 1

        name_slice := buf[name_start..name_start + name_len]
        val_slice := buf[vs..ve]
        if name_len == 4 && EqIgnoreAscii(name_slice, b"host") {
            if has_host || ve == vs {
                return false
            }
            has_host = true
        } else if name_len == 14 && EqIgnoreAscii(name_slice, b"content-length") {
            if ve == vs {
                return false
            }
            var v := 0
            for i in vs..ve {
                c := buf[i]
                if c < 48 || c > 57 {
                    return false
                }
                v = v * 10 + int(c - 48)
                if v > 1_073_741_824 {
                    return false
                }
            }
            if has_cl && v != cl_val {
                return false
            }
            has_cl = true
            cl_val = v
        } else if name_len == 17 && EqIgnoreAscii(name_slice, b"transfer-encoding") {
            if chunked || !EqIgnoreAscii(val_slice, b"chunked") {
                return false
            }
            chunked = true
        } else if name_len == 10 && EqIgnoreAscii(name_slice, b"connection") {
            if EqIgnoreAscii(val_slice, b"close") {
                keep_alive = false
            } else if EqIgnoreAscii(val_slice, b"keep-alive") {
                keep_alive = true
            }
        }
        pos = pos + ln_rel + 1
    }

    if !head_done || !has_host || (has_cl && chunked) {
        return false
    }
    out.nhdrs = nhdrs
    out.chunked = chunked
    out.keep_alive = keep_alive

    if chunked {
        body_buf.Clear()
        for {
            if pos >= n {
                return false
            }
            sz_rel := simd.IndexByte(buf[pos..n], 10) else {
                return false
            }
            if sz_rel < 1 {
                return false
            }
            sz_end := pos + sz_rel - 1
            if buf[sz_end] != 13 {
                return false
            }
            var sz := 0
            var digits := 0
            var k := pos
            for k < sz_end {
                c := buf[k]
                if c == 59 {
                    break
                }
                hv := HexVal(c)
                if hv < 0 {
                    return false
                }
                sz = sz * 16 + hv
                if sz > 16_777_216 {
                    return false
                }
                digits += 1
                k += 1
            }
            if digits == 0 {
                return false
            }
            pos = pos + sz_rel + 1
            if sz == 0 {
                if pos + 2 != n || buf[pos] != 13 || buf[pos + 1] != 10 {
                    return false
                }
                break
            }
            if pos + sz + 2 > n {
                return false
            }
            for j in 0..sz {
                try body_buf.Push(buf[pos + j])
            }
            pos += sz
            if buf[pos] != 13 || buf[pos + 1] != 10 {
                return false
            }
            pos += 2
        }
        out.body_off = 0
        out.body_len = body_buf.Len()
        out.body_from_buf = true
    } else if has_cl {
        if n - pos != cl_val {
            return false
        }
        out.body_off = pos
        out.body_len = cl_val
        out.body_from_buf = false
    } else {
        if pos != n {
            return false
        }
        out.body_off = 0
        out.body_len = 0
        out.body_from_buf = false
    }
    return true
}

FnvByte(h: u64, b: u8): u64 {
    return (h ^ u64(b)) *% FnvPrime
}

FnvBytes(h: u64, s: []u8): u64 {
    var acc := h
    for i in 0..s.Len() {
        acc = (acc ^ u64(s[i])) *% FnvPrime
    }
    return acc
}

FnvBytesLower(h: u64, s: []u8): u64 {
    var acc := h
    for i in 0..s.Len() {
        acc = (acc ^ u64(LowerAscii(s[i]))) *% FnvPrime
    }
    return acc
}

FmtHex16(v: u64, out: mut []u8) {
    hex := b"0123456789abcdef"
    var i := 15
    var x := v
    for i >= 0 {
        out[i] = hex[Cast[int](x & 15)]
        x = x >> 4
        i -= 1
    }
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError|Utf8Error!void {
    if sys.args.Len() != 4 {
        var e := sys.Stderr()
        try Print(mut e, "usage: httpparse <num_requests> <rounds> <seed>\n")
        fail ParseError
    }
    num_req := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    seed := Cast[u64](try sys.args[3].ParseInt())

    var a := try Arena.New(sys.heap)
    defer a.Free()

    var data := List[u8].New(&a)
    var starts := List[int].New(&a)
    try BuildBatch(num_req, seed, mut data, mut starts)

    raw := data.Items()
    offs := starts.Items()
    hdrs := try a.Slice[HeaderSpan](MaxHeaders)
    var body_buf := List[u8].New(&a)
    var parsed := Parsed(
        method: SliceSpan(off: 0, len: 0),
        path: SliceSpan(off: 0, len: 0),
        query: SliceSpan(off: 0, len: 0),
        nhdrs: 0,
        body_off: 0,
        body_len: 0,
        body_from_buf: false,
        chunked: false,
        keep_alive: false,
    )

    var ok_cnt: i64 = 0
    var err_cnt: i64 = 0
    var hdr_cnt: i64 = 0
    var body_cnt: i64 = 0
    var chunked_cnt: i64 = 0
    var ka_cnt: i64 = 0
    var h: u64 = FnvBasis

    for _ in 0..rounds {
        for i in 0..num_req {
            req_buf := raw[offs[i]..offs[i + 1]]
            if try ParseRequest(req_buf, hdrs, mut body_buf, mut parsed) {
                ok_cnt += 1
                hdr_cnt += i64(parsed.nhdrs)
                body_cnt += i64(parsed.body_len)
                if parsed.chunked {
                    chunked_cnt += 1
                }
                if parsed.keep_alive {
                    ka_cnt += 1
                }
                h = FnvBytes(h, req_buf[parsed.method.off..parsed.method.off + parsed.method.len])
                h = FnvByte(h, 1)
                h = FnvBytes(h, req_buf[parsed.path.off..parsed.path.off + parsed.path.len])
                h = FnvByte(h, 2)
                h = FnvBytes(h, req_buf[parsed.query.off..parsed.query.off + parsed.query.len])
                h = FnvByte(h, 3)
                for k in 0..parsed.nhdrs {
                    hs := hdrs[k]
                    h = FnvBytesLower(h, req_buf[hs.name.off..hs.name.off + hs.name.len])
                    h = FnvByte(h, 4)
                    h = FnvBytes(h, req_buf[hs.value.off..hs.value.off + hs.value.len])
                    h = FnvByte(h, 5)
                }
                if parsed.body_from_buf {
                    h = FnvBytes(h, body_buf.Items())
                } else {
                    h = FnvBytes(h, req_buf[parsed.body_off..parsed.body_off + parsed.body_len])
                }
                h = FnvByte(h, 6)
            } else {
                err_cnt += 1
                h = FnvByte(h, 255)
                h = FnvByte(h, Cast[u8](i % 251))
            }
        }
    }

    var hex_buf: [16]u8 = [0; 16]
    FmtHex16(h, mut hex_buf[..])
    fnv_str := try str.From(hex_buf[..])
    var out := sys.Stdout()
    try Print(mut out, "ok={ok_cnt} err={err_cnt} hdrs={hdr_cnt} body={body_cnt} chunked={chunked_cnt} keepalive={ka_cnt} fnv={fnv_str}\n", (ok_cnt, err_cnt, hdr_cnt, body_cnt, chunked_cnt, ka_cnt, fnv_str))
}
