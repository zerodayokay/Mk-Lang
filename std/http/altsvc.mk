// Why: a TLS reply can advertise the HTTP/3 listener for its origin.
// What: the server's Alt-Svc response field.
// How: preserve an explicit field and advertise the actual bound UDP port.

import std.time

Response.AdvertiseH3(mut self, port: int): OutOfMemory!void {
    if port == 0 || self.fl.head {
        return
    }
    for f in self.headers.Items() {
        if EqFold(f.name, "alt-svc") {
            return
        }
    }
    value := try self.arena.Fmt("h3=\":{port}\"; ma=86400", (port))
    try self.Header("alt-svc", value)
}

const AltOrigins = 64
const AltCount = 8
const AltMaxAge: i64 = 2147483647

struct AltPort {
    port: int
    age: i64
}

const NoAltPort = AltPort(port: 0, age: 0)

struct AltRoutes {
    ports: [8]AltPort
    n: int
    clear: bool
}

const NoAltRoutes = AltRoutes(ports: [NoAltPort; 8], n: 0, clear: false)

struct AltOrigin {
    key: Origin
    routes: AltRoutes
    received: ?time.Instant
    broken: ?time.Instant
}

const NoAltOrigin = AltOrigin(key: Origin(b: [0; 280], n: -1), routes: NoAltRoutes, received: none, broken: none)

struct AltScanner {
    input: str
    i: int
}

AltScanner.Spaces(mut self) {
    b := self.input.AsBytes()
    for self.i < b.Len() && (b[self.i] == 32 || b[self.i] == 9) {
        self.i += 1
    }
}

AltScanner.Take(mut self, byte: u8): bool {
    self.Spaces()
    if self.i == self.input.Len() || self.input.AsBytes()[self.i] != byte {
        return false
    }
    self.i += 1
    return true
}

AltScanner.Token(mut self): (start: int, end: int) {
    self.Spaces()
    at := self.i
    b := self.input.AsBytes()
    for self.i < b.Len() && AltToken(b[self.i]) {
        self.i += 1
    }
    return (start: at, end: self.i)
}

AltToken(b: u8): bool {
    return b >= b'a' && b <= b'z' || b >= b'A' && b <= b'Z' || b >= b'0' && b <= b'9' || b == b'!' || b == b'#' || b == b'$' || b == b'%' || b == b'&' || b == b'\'' || b == b'*' || b == b'+' || b == b'-' || b == b'.' || b == b'^' || b == b'_' || b == 96 || b == b'|' || b == b'~'
}

// Finishes an RFC 9110 quoted string after the opening quote. Unknown
// parameters need no buffer bigger than the authority's bounded buffer.
AltScanner.Quoted(mut self, buf: mut []u8): ?int {
    b := self.input.AsBytes()
    var n := 0
    for self.i < b.Len() {
        var v := b[self.i]
        self.i += 1
        if v == b'"' {
            return some(n)
        }
        if v == 92 {
            if self.i == b.Len() {
                return none
            }
            v = b[self.i]
            self.i += 1
        }
        if v < 32 && v != 9 || v == 127 {
            return none
        }
        if n < buf.Len() {
            buf[n] = v
        }
        n += 1
    }
    return none
}

// AltSeconds parses delta-seconds and saturates at RFC 9111 1.2.2's
// overflow value. Runs in O(value length) and allocates nothing.
AltSeconds(b: []u8): ?i64 {
    if b.Len() == 0 {
        return none
    }
    var n: i64 = 0
    for v in b {
        if v < b'0' || v > b'9' {
            return none
        }
        if n < AltMaxAge {
            n = n * 10 + i64(v - b'0')
            if n > AltMaxAge {
                n = AltMaxAge
            }
        }
    }
    return some(n)
}

AltAuthorityPort(b: []u8, host: str): int {
    var colon := b.Len()
    for colon > 0 && b[colon - 1] != b':' {
        colon -= 1
    }
    if colon == 0 || colon == b.Len() || b.Len() - colon > 5 {
        return 0
    }
    if colon > 1 {
        var start := 0
        var end := colon - 1
        if b[0] == b'[' && b[end - 1] == b']' {
            start = 1
            end -= 1
        }
        name := str.From(b[start..end]) else { return 0 }
        if !EqFold(name, host) {
            return 0
        }
    }
    p := AltSeconds(b[colon..]) else { return 0 }
    return if p <= 65535 { int(p) } else { 0 }
}

// ParseAltSvc scans one field in O(value length), retaining at most eight
// same-host h3 alternatives in wire order and allocating nothing.
ParseAltSvc(input: str, host: str, age: i64): ?AltRoutes {
    var p := AltScanner(input: input, i: 0)
    var result := NoAltRoutes
    var scratch: [280]u8 = [0; 280]
    bytes := input.AsBytes()
    for {
        p.Spaces()
        if p.i == input.Len() {
            return some(result)
        }
        if p.Take(b',') {
            continue
        }
        t := p.Token()
        if t.start == t.end {
            return none
        }
        proto := str.From(bytes[t.start..t.end]) else { return none }
        if proto == "clear" {
            result.clear = true
        } else {
            if !p.Take(b'=') || !p.Take(b'"') {
                return none
            }
            n := p.Quoted(mut scratch[..]) else { return none }
            port := if proto == "h3" && n <= scratch.Len() { AltAuthorityPort(scratch[0..n], host) } else { 0 }
            var ma: i64 = 86400
            for p.Take(b';') {
                k := p.Token()
                if k.start == k.end || !p.Take(b'=') {
                    return none
                }
                key := str.From(bytes[k.start..k.end]) else { return none }
                if p.Take(b'"') {
                    q := p.Quoted(mut scratch[..]) else { return none }
                    if EqFold(key, "ma") {
                        if q > scratch.Len() {
                            return none
                        }
                        ma = AltSeconds(scratch[0..q]) else { return none }
                    }
                } else {
                    v := p.Token()
                    if v.start == v.end {
                        return none
                    }
                    if EqFold(key, "ma") {
                        ma = AltSeconds(bytes[v.start..v.end]) else { return none }
                    }
                }
            }
            if port != 0 && ma > age && result.n < AltCount {
                result.ports[result.n] = AltPort(port: port, age: ma - age)
                result.n += 1
            }
        }
        if p.i == input.Len() {
            return some(result)
        }
        if !p.Take(b',') {
            return none
        }
    }
}

// Finds the origin or an entry to overwrite. All scans are bounded by
// AltOrigins and the lock is held only over plain cache data.
AltIndex(cache: &[64]AltOrigin, key: &Origin, now: time.Instant): int {
    var oldest := 0
    var age: i64 = -1
    for i in 0..AltOrigins {
        e := &cache[i]
        if OriginEq(&e.key, key) {
            return i
        }
        instant := if t := e.received { some(t) } else { e.broken }
        elapsed := if t := instant { now.Since(t).Nanos() } else { i64(MaxBody) }
        if elapsed > age {
            oldest = i
            age = elapsed
        }
    }
    return oldest
}

Client.H3Routes(&self, io: Io, key: &Origin, port: int): AltRoutes {
    nc := io.NoCancel()
    now := time.Now(nc)
    g := self.alt_cache.Lock(nc).Must()
    e := &(*g.Ref())[AltIndex(g.Ref(), key, now)]
    var routes := NoAltRoutes
    same := OriginEq(&e.key, key)
    if same {
        if t := e.broken {
            if now.Since(t).Nanos() < time.Minutes(5).Nanos() {
                g.Unlock()
                routes.clear = true
                return routes
            }
        }
        if t := e.received {
            age := now.Since(t).Nanos()
            for i in 0..e.routes.n {
                r := e.routes.ports[i]
                if age < time.Seconds(r.age).Nanos() {
                    routes.ports[routes.n] = r
                    routes.n += 1
                }
            }
        }
    }
    g.Unlock()
    if self.limits.h3_direct && routes.n == 0 {
        routes.ports[0] = AltPort(port: port, age: 0)
        routes.n = 1
    }
    return routes
}

Client.H3Broken(&self, io: Io, key: Origin) {
    nc := io.NoCancel()
    now := time.Now(nc)
    var g := self.alt_cache.Lock(nc).Must()
    i := AltIndex(g.Ref(), &key, now)
    e := mut (*g.Mut())[i]
    if !OriginEq(&e.key, &key) {
        *e = NoAltOrigin
        e.key = key
    }
    e.broken = some(now)
    g.Unlock()
}

Client.RememberAltSvc(&self, io: Io, host: str, key: Origin, ex: &Exchange) {
    if !self.limits.alt_svc || self.limits.h3.IsNone() || self.limits.proxy.IsSome() || key.n < 0 {
        return
    }
    var incoming := NoAltRoutes
    var found := ex.status == 421 && ex.version == HttpVersion.Http3
    if ex.status != 421 {
        var age: i64 = 0
        for f in ex.headers {
            if EqFold(f.name, "age") {
                age = AltSeconds(f.value.AsBytes()) else { 0 }
                break
            }
        }
        for f in ex.headers {
            if !EqFold(f.name, "alt-svc") {
                continue
            }
            if routes := ParseAltSvc(f.value, host, age) {
                found = true
                if routes.clear {
                    incoming = NoAltRoutes
                    break
                }
                for j in 0..routes.n {
                    if incoming.n == AltCount {
                        break
                    }
                    incoming.ports[incoming.n] = routes.ports[j]
                    incoming.n += 1
                }
            }
        }
    } else if found {
        self.H3Retire(io, &key)
    }
    if found {
        nc := io.NoCancel()
        now := time.Now(nc)
        var g := self.alt_cache.Lock(nc).Must()
        i := AltIndex(g.Ref(), &key, now)
        e := mut (*g.Mut())[i]
        if !OriginEq(&e.key, &key) {
            *e = NoAltOrigin
            e.key = key
        }
        e.routes = incoming
        e.received = some(now)
        g.Unlock()
    }
}
