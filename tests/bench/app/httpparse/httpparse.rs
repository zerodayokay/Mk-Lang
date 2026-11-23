// httpparse, Rust, std: the task of README.md, a strict RFC 9112 HTTP/1.1
// request parser over a splitmix64-generated batch of valid and malformed
// requests (GET/POST/PUT/DELETE, paths and query strings, headers, fixed
// Content-Length and chunked bodies, plus 10 malformed classes). Pure safe Rust
// (no unsafe, no hints, no get_unchecked), zero external crates, reusing header
// array and chunked body Vec across requests. Wrapping u64 FNV-1a digest.
// Usage: httpparse <num_requests> <rounds> <seed>.
use std::env;
use std::process;

const FNV_BASIS: u64 = 0xcbf29ce484222325;
const FNV_PRIME: u64 = 0x100000001b3;
const MAX_HEADERS: usize = 64;

struct Rng {
    s: u64,
}

impl Rng {
    fn next(&mut self) -> u64 {
        self.s = self.s.wrapping_add(0x9e3779b97f4a7c15);
        let mut z = self.s;
        z = (z ^ (z >> 30)).wrapping_mul(0xbf58476d1ce4e5b9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94d049bb133111eb);
        z ^ (z >> 31)
    }
}

fn push_u64(dst: &mut Vec<u8>, mut x: u64) {
    if x == 0 {
        dst.push(b'0');
        return;
    }
    let mut tmp = [0u8; 24];
    let mut n = 0;
    while x > 0 {
        tmp[n] = b'0' + (x % 10) as u8;
        x /= 10;
        n += 1;
    }
    for i in (0..n).rev() {
        dst.push(tmp[i]);
    }
}

fn push_hex(dst: &mut Vec<u8>, mut x: usize) {
    const HEX: &[u8] = b"0123456789abcdef";
    if x == 0 {
        dst.push(b'0');
        return;
    }
    let mut tmp = [0u8; 16];
    let mut n = 0;
    while x > 0 {
        tmp[n] = HEX[x & 15];
        x >>= 4;
        n += 1;
    }
    for i in (0..n).rev() {
        dst.push(tmp[i]);
    }
}

fn build_batch(n: usize, seed: u64) -> (Vec<u8>, Vec<usize>) {
    let mut rng = Rng { s: seed };
    let mut data = Vec::with_capacity(n * 384);
    let mut starts = Vec::with_capacity(n + 1);
    for _ in 0..n {
        starts.push(data.len());
        let r = rng.next();
        if r % 20 == 0 {
            let bad: &[u8] = match (r >> 8) % 10 {
                0 => b"GET /api/v1/bad HTTP/2.0\r\nHost: api.example.com\r\n\r\n",
                1 => b"GET /api/v1/bad HTTP/1.1\nHost: api.example.com\r\n\r\n",
                2 => b"GET /api/v1/bad HTTP/1.1\r\nHost : api.example.com\r\n\r\n",
                3 => b"GET /api/v1/bad HTTP/1.1\r\nUser-Agent: bench/1.0\r\n\r\n",
                4 => b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n0\r\n\r\n",
                5 => b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\nabcd",
                6 => b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 12abc\r\n\r\nabcd",
                7 => b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 20\r\n\r\nshort",
                8 => b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\nabcd\r\n0\r\n\r\n",
                _ => b"POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n",
            };
            data.extend_from_slice(bad);
            continue;
        }
        let m = (r >> 5) % 4;
        match m {
            0 => data.extend_from_slice(b"GET "),
            1 => data.extend_from_slice(b"POST "),
            2 => data.extend_from_slice(b"PUT "),
            _ => data.extend_from_slice(b"DELETE "),
        }
        data.extend_from_slice(b"/api/v1/users/");
        push_u64(&mut data, (r >> 8) % 10000);
        data.extend_from_slice(b"/items/");
        push_u64(&mut data, (r >> 22) % 100000);
        if ((r >> 40) & 1) == 1 {
            data.extend_from_slice(b"?page=");
            push_u64(&mut data, (r >> 41) % 100);
            data.extend_from_slice(b"&limit=");
            push_u64(&mut data, 10 + ((r >> 48) % 90));
            data.extend_from_slice(b"&sort=asc");
        }
        data.extend_from_slice(b" HTTP/1.1\r\nHost: api");
        push_u64(&mut data, r & 7);
        data.extend_from_slice(b".example.com\r\nUser-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) BenchAgent/1.0\r\nAccept: application/json, text/plain;q=0.9, */*;q=0.8\r\nAccept-Encoding: gzip, deflate, br\r\n");
        if ((r >> 39) & 1) == 1 {
            data.extend_from_slice(b"Connection: close\r\n");
        } else {
            data.extend_from_slice(b"Connection: keep-alive\r\n");
        }
        let r2 = rng.next();
        data.extend_from_slice(b"X-Request-Id: req-");
        push_u64(&mut data, r2 % 100000000);
        data.extend_from_slice(b"\r\n");
        if m == 1 || m == 2 {
            data.extend_from_slice(b"Content-Type: application/json\r\n");
            if ((r2 >> 30) & 1) == 1 {
                data.extend_from_slice(b"Transfer-Encoding: chunked\r\n\r\n");
                let nchunks = 2 + ((r2 >> 32) % 4) as usize;
                for ci in 0..nchunks {
                    let shift = (ci * 7) as u32;
                    let clen = 16 + (((r2 >> shift) ^ (ci as u64 * 13)) % 64) as usize;
                    push_hex(&mut data, clen);
                    data.extend_from_slice(b"\r\n");
                    for j in 0..clen {
                        data.push(b'a' + ((ci * 7 + j) % 26) as u8);
                    }
                    data.extend_from_slice(b"\r\n");
                }
                data.extend_from_slice(b"0\r\n\r\n");
            } else {
                let blen = 32 + ((r2 >> 32) % 224) as usize;
                data.extend_from_slice(b"Content-Length: ");
                push_u64(&mut data, blen as u64);
                data.extend_from_slice(b"\r\n\r\n");
                let off = (r2 % 26) as usize;
                for j in 0..blen {
                    data.push(b'a' + ((j + off) % 26) as u8);
                }
            }
        } else {
            data.extend_from_slice(b"\r\n");
        }
    }
    starts.push(data.len());
    (data, starts)
}

#[derive(Clone, Copy, Default)]
struct SliceSpan {
    off: usize,
    len: usize,
}

#[derive(Clone, Copy, Default)]
struct HeaderSpan {
    name: SliceSpan,
    value: SliceSpan,
}

#[derive(Default)]
struct Parsed {
    method: SliceSpan,
    path: SliceSpan,
    query: SliceSpan,
    nhdrs: usize,
    body_off: usize,
    body_len: usize,
    body_from_buf: bool,
    chunked: bool,
    keep_alive: bool,
}

fn is_tchar(b: u8) -> bool {
    b.is_ascii_alphanumeric()
        || matches!(
            b,
            b'!' | b'#' | b'$' | b'%' | b'&' | b'\'' | b'*' | b'+' | b'-' | b'.' | b'^' | b'_' | b'`' | b'|' | b'~'
        )
}

fn hex_val(b: u8) -> Option<usize> {
    match b {
        b'0'..=b'9' => Some((b - b'0') as usize),
        b'a'..=b'f' => Some((b - b'a' + 10) as usize),
        b'A'..=b'F' => Some((b - b'A' + 10) as usize),
        _ => None,
    }
}

fn find_byte(s: &[u8], needle: u8) -> Option<usize> {
    s.iter().position(|&b| b == needle)
}

fn parse_request(buf: &[u8], hdrs: &mut [HeaderSpan; MAX_HEADERS], body_buf: &mut Vec<u8>, out: &mut Parsed) -> bool {
    let n = buf.len();
    let Some(nl) = find_byte(buf, b'\n') else { return false; };
    if nl < 1 || buf[nl - 1] != b'\r' {
        return false;
    }
    let line_end = nl - 1;
    let Some(sp1) = find_byte(&buf[..line_end], b' ') else { return false; };
    if sp1 == 0 || !buf[..sp1].iter().all(|&b| is_tchar(b)) {
        return false;
    }
    let rest1 = sp1 + 1;
    let Some(sp2_rel) = find_byte(&buf[rest1..line_end], b' ') else { return false; };
    if sp2_rel == 0 {
        return false;
    }
    let sp2 = rest1 + sp2_rel;
    if buf[rest1] != b'/' {
        return false;
    }
    let mut qpos: Option<usize> = None;
    for i in rest1..sp2 {
        let c = buf[i];
        if !(0x21..=0x7e).contains(&c) {
            return false;
        }
        if c == b'?' && qpos.is_none() {
            qpos = Some(i);
        }
    }
    let ver_start = sp2 + 1;
    if &buf[ver_start..line_end] != b"HTTP/1.1" {
        return false;
    }

    out.method = SliceSpan { off: 0, len: sp1 };
    if let Some(q) = qpos {
        out.path = SliceSpan { off: rest1, len: q - rest1 };
        out.query = SliceSpan { off: q + 1, len: sp2 - (q + 1) };
    } else {
        out.path = SliceSpan { off: rest1, len: sp2 - rest1 };
        out.query = SliceSpan { off: 0, len: 0 };
    }

    let mut pos = nl + 1;
    let mut nhdrs = 0usize;
    let mut has_host = false;
    let mut has_cl = false;
    let mut cl_val = 0usize;
    let mut chunked = false;
    let mut keep_alive = true;
    let mut head_done = false;

    while pos < n {
        let Some(ln_rel) = find_byte(&buf[pos..], b'\n') else { return false; };
        if ln_rel < 1 {
            return false;
        }
        let ln_end = pos + ln_rel - 1;
        if buf[ln_end] != b'\r' {
            return false;
        }
        if ln_end == pos {
            pos += ln_rel + 1;
            head_done = true;
            break;
        }
        if buf[pos] == b' ' || buf[pos] == b'\t' {
            return false;
        }
        let Some(colon_rel) = find_byte(&buf[pos..ln_end], b':') else { return false; };
        if colon_rel == 0 {
            return false;
        }
        let name_start = pos;
        let name_len = colon_rel;
        if !buf[name_start..name_start + name_len].iter().all(|&b| is_tchar(b)) {
            return false;
        }
        let mut vs = name_start + name_len + 1;
        let mut ve = ln_end;
        while vs < ve && (buf[vs] == b' ' || buf[vs] == b'\t') {
            vs += 1;
        }
        while ve > vs && (buf[ve - 1] == b' ' || buf[ve - 1] == b'\t') {
            ve -= 1;
        }
        for &c in &buf[vs..ve] {
            if (c < 0x20 && c != b'\t') || c == 0x7f {
                return false;
            }
        }
        if nhdrs >= MAX_HEADERS {
            return false;
        }
        hdrs[nhdrs] = HeaderSpan {
            name: SliceSpan { off: name_start, len: name_len },
            value: SliceSpan { off: vs, len: ve - vs },
        };
        nhdrs += 1;

        let name = &buf[name_start..name_start + name_len];
        let val = &buf[vs..ve];
        if name.eq_ignore_ascii_case(b"host") {
            if has_host || val.is_empty() {
                return false;
            }
            has_host = true;
        } else if name.eq_ignore_ascii_case(b"content-length") {
            if val.is_empty() {
                return false;
            }
            let mut v = 0usize;
            for &c in val {
                if !c.is_ascii_digit() {
                    return false;
                }
                v = v * 10 + (c - b'0') as usize;
                if v > 1_073_741_824 {
                    return false;
                }
            }
            if has_cl && v != cl_val {
                return false;
            }
            has_cl = true;
            cl_val = v;
        } else if name.eq_ignore_ascii_case(b"transfer-encoding") {
            if chunked || !val.eq_ignore_ascii_case(b"chunked") {
                return false;
            }
            chunked = true;
        } else if name.eq_ignore_ascii_case(b"connection") {
            if val.eq_ignore_ascii_case(b"close") {
                keep_alive = false;
            } else if val.eq_ignore_ascii_case(b"keep-alive") {
                keep_alive = true;
            }
        }
        pos += ln_rel + 1;
    }

    if !head_done || !has_host || (has_cl && chunked) {
        return false;
    }
    out.nhdrs = nhdrs;
    out.chunked = chunked;
    out.keep_alive = keep_alive;

    if chunked {
        body_buf.clear();
        loop {
            if pos >= n {
                return false;
            }
            let Some(sz_rel) = find_byte(&buf[pos..], b'\n') else { return false; };
            if sz_rel < 1 {
                return false;
            }
            let sz_end = pos + sz_rel - 1;
            if buf[sz_end] != b'\r' {
                return false;
            }
            let mut sz = 0usize;
            let mut digits = 0usize;
            for &c in &buf[pos..sz_end] {
                if c == b';' {
                    break;
                }
                let Some(hv) = hex_val(c) else { return false; };
                sz = sz * 16 + hv;
                if sz > 16_777_216 {
                    return false;
                }
                digits += 1;
            }
            if digits == 0 {
                return false;
            }
            pos += sz_rel + 1;
            if sz == 0 {
                if pos + 2 != n || buf[pos] != b'\r' || buf[pos + 1] != b'\n' {
                    return false;
                }
                break;
            }
            if pos + sz + 2 > n {
                return false;
            }
            body_buf.extend_from_slice(&buf[pos..pos + sz]);
            pos += sz;
            if buf[pos] != b'\r' || buf[pos + 1] != b'\n' {
                return false;
            }
            pos += 2;
        }
        out.body_off = 0;
        out.body_len = body_buf.len();
        out.body_from_buf = true;
    } else if has_cl {
        if n - pos != cl_val {
            return false;
        }
        out.body_off = pos;
        out.body_len = cl_val;
        out.body_from_buf = false;
    } else {
        if pos != n {
            return false;
        }
        out.body_off = 0;
        out.body_len = 0;
        out.body_from_buf = false;
    }
    true
}

fn fnv_byte(h: u64, b: u8) -> u64 {
    (h ^ b as u64).wrapping_mul(FNV_PRIME)
}

fn fnv_bytes(mut h: u64, s: &[u8]) -> u64 {
    for &b in s {
        h = (h ^ b as u64).wrapping_mul(FNV_PRIME);
    }
    h
}

fn fnv_bytes_lower(mut h: u64, s: &[u8]) -> u64 {
    for &b in s {
        h = (h ^ b.to_ascii_lowercase() as u64).wrapping_mul(FNV_PRIME);
    }
    h
}

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() != 4 {
        eprintln!("usage: httpparse <num_requests> <rounds> <seed>");
        process::exit(2);
    }
    let num_req: usize = args[1].parse().expect("num_requests");
    let rounds: usize = args[2].parse().expect("rounds");
    let seed: u64 = args[3].parse().expect("seed");

    let (data, starts) = build_batch(num_req, seed);
    let mut hdrs = [HeaderSpan::default(); MAX_HEADERS];
    let mut body_buf = Vec::with_capacity(1024);
    let mut parsed = Parsed::default();

    let mut ok_cnt: i64 = 0;
    let mut err_cnt: i64 = 0;
    let mut hdr_cnt: i64 = 0;
    let mut body_cnt: i64 = 0;
    let mut chunked_cnt: i64 = 0;
    let mut ka_cnt: i64 = 0;
    let mut h: u64 = FNV_BASIS;

    for _ in 0..rounds {
        for i in 0..num_req {
            let req_buf = &data[starts[i]..starts[i + 1]];
            if parse_request(req_buf, &mut hdrs, &mut body_buf, &mut parsed) {
                ok_cnt += 1;
                hdr_cnt += parsed.nhdrs as i64;
                body_cnt += parsed.body_len as i64;
                if parsed.chunked {
                    chunked_cnt += 1;
                }
                if parsed.keep_alive {
                    ka_cnt += 1;
                }
                h = fnv_bytes(h, &req_buf[parsed.method.off..parsed.method.off + parsed.method.len]);
                h = fnv_byte(h, 1);
                h = fnv_bytes(h, &req_buf[parsed.path.off..parsed.path.off + parsed.path.len]);
                h = fnv_byte(h, 2);
                h = fnv_bytes(h, &req_buf[parsed.query.off..parsed.query.off + parsed.query.len]);
                h = fnv_byte(h, 3);
                for k in 0..parsed.nhdrs {
                    let hs = hdrs[k];
                    h = fnv_bytes_lower(h, &req_buf[hs.name.off..hs.name.off + hs.name.len]);
                    h = fnv_byte(h, 4);
                    h = fnv_bytes(h, &req_buf[hs.value.off..hs.value.off + hs.value.len]);
                    h = fnv_byte(h, 5);
                }
                if parsed.body_from_buf {
                    h = fnv_bytes(h, &body_buf);
                } else {
                    h = fnv_bytes(h, &req_buf[parsed.body_off..parsed.body_off + parsed.body_len]);
                }
                h = fnv_byte(h, 6);
            } else {
                err_cnt += 1;
                h = fnv_byte(h, 0xff);
                h = fnv_byte(h, (i % 251) as u8);
            }
        }
    }

    println!(
        "ok={} err={} hdrs={} body={} chunked={} keepalive={} fnv={:016x}",
        ok_cnt, err_cnt, hdr_cnt, body_cnt, chunked_cnt, ka_cnt, h
    );
}
