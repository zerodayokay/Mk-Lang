// Why: the HTTP implementations each own framing on their side of the relay.
// What: ConnectionFields and ForwardField, the forwarding policy.
// How: remove transport fields and any fields named by Connection, and
// let the new exchange supply its own authority, framing and diagnostics.

import std.http

ConnectionFields(a: &Arena, headers: []http.Header): OutOfMemory!Map[str, bool] from a {
    var local := Map[str, bool].New(a)
    for h in headers {
        if SameName(h.name, "connection") {
            for token in h.value.Split(",") {
                try local.Set(try token.Trim().ToLower(a), true)
            }
        }
    }
    return local
}

ForwardField(a: &Arena, name: str, local: &Map[str, bool]): OutOfMemory!bool {
    folded := try name.ToLower(a)
    return match folded {
        "host" | "content-length" | "connection" | "proxy-connection" |
            "keep-alive" | "te" | "trailer" | "transfer-encoding" | "upgrade" |
            "proxy-authorization" | "proxy-authenticate" | "alt-svc" |
            "x-relay-ingress" { false }
        _ { !local.Has(&folded) }
    }
}

SameName(a: str, b: str): bool {
    if a.Len() != b.Len() {
        return false
    }
    for i in 0..a.Len() {
        if Fold(a.AsBytes()[i]) != Fold(b.AsBytes()[i]) {
            return false
        }
    }
    return true
}

Fold(c: u8): u8 {
    return if c >= b'A' && c <= b'Z' { c + 32 } else { c }
}

HasField(headers: []http.Header, name: str, value: str): bool {
    for h in headers {
        if SameName(h.name, name) && h.value == value {
            return true
        }
    }
    return false
}
