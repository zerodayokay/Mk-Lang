# app/httpparse: strict RFC 9112 HTTP/1.1 request parser

An `app` row (tests/bench/README.md "Applications"). Every language implements a strict RFC 9112 HTTP/1.1 request parser over a deterministically generated batch of realistic valid and malformed requests, decoding headers, fixed `Content-Length` bodies and `Transfer-Encoding: chunked` bodies, rejecting malformed/smuggled inputs, and folding parsed fields into counts and a wrapping FNV-1a 64 digest. The parser itself is the row's task, so every cell is kind `std` with zero external dependencies.

## Command line

```
httpparse <num_requests> <rounds> <seed>
```

Prints one line:

```
ok=<n> err=<n> hdrs=<n> body=<bytes> chunked=<n> keepalive=<n> fnv=<16 hex digits>
```

## Input generation

From `splitmix64(seed)`, the program builds `<num_requests>` contiguous raw HTTP/1.1 request buffers:
- ~5% (`r % 20 == 0`) malformed requests across 10 distinct rejection classes: wrong HTTP version (`HTTP/2.0`), bare LF in request line, whitespace before header colon (smuggling), missing required `Host` header, conflicting `Content-Length` + `Transfer-Encoding: chunked`, duplicate differing `Content-Length`, non-numeric `Content-Length`, truncated fixed body, invalid chunk hex size, and truncated chunked stream.
- ~95% valid HTTP/1.1 requests across `GET`, `POST`, `PUT`, `DELETE`, origin-form paths `/api/v1/users/<id>/items/<id>` with optional `?page=...&limit=...&sort=asc` query strings, 6-8 headers (`Host`, `User-Agent`, `Accept`, `Accept-Encoding`, `Connection: keep-alive|close`, `X-Request-Id`, and for `POST`/`PUT` `Content-Type` plus either fixed `Content-Length` 32..255 bytes or `Transfer-Encoding: chunked` across 2..5 chunks).

## Parsing contract (every step)

For `<rounds>` passes over all `<num_requests>` buffers:
1. **Request line:** require CRLF termination (reject bare LF), validate RFC 9110 `tchar` method, origin-form target starting with `/` of visible ASCII (`0x21..0x7E`) split at the first `?` into `path` and `query`, and exact version `HTTP/1.1`.
2. **Headers:** parse up to 64 `name: value\r\n` lines until the blank `\r\n`; reject obs-fold (leading SP/HTAB), whitespace before `:`, non-`tchar` names, and control bytes (`< 0x20` except HTAB, or `0x7F`) in values; trim leading/trailing OWS from values.
3. **Framing (RFC 9112 §6.3):** require exactly one non-empty `Host`; parse strict ASCII decimal `Content-Length` (reject differing duplicates); accept lone `Transfer-Encoding: chunked`; reject combined `Content-Length` + `Transfer-Encoding`; track `Connection: close|keep-alive`.
4. **Body:** decode chunked framing (hex sizes, optional chunk extensions `;...`, per-chunk CRLF, final `0\r\n\r\n`, no trailing bytes) into a reused buffer, or verify exact remaining bytes for `Content-Length` / empty body.
5. **Aggregate:** fold valid requests (`method`, `path`, `query`, lowercased header names, trimmed values, decoded body, with domain-separator bytes `1..6`) and rejected requests (`0xFF`, `i % 251`) into a wrapping FNV-1a 64 digest (`basis = 0xcbf29ce484222325`, `prime = 0x100000001b3`).

## Cells

| Language | Kind | Program | Libraries |
|---|---|---|---|
| Mk | `std` | `mk/httpparse.mk`: `std.simd.IndexByte` (allowed std dispatch routine), arena-allocated header slice and reused chunked body `List[u8]` | std only |
| C | `std` | `httpparse.c`: `memchr`, flat arrays, reused body buffer | std only |
| Rust | `std` | `httpparse.rs`: slice `.iter().position()`, reused header array and `Vec<u8>` (no `unsafe`, no hints) | std only |
| Zig | `std` | `httpparse.zig`: `std.mem.indexOfScalar`, reused header array and `std.ArrayList(u8)` | std only |
| Go | `std` | `testdata/httpparse.go`: `bytes.IndexByte`, reused header array and `[]byte` | std only |
| C# | `std` | `httpparse.cs`: `ReadOnlySpan<byte>.IndexOf`, reused header array and `ByteBuf` | std only |

- **Overflow class:** `wraps` (FNV-1a 64 wrapping multiplication).
- **Environment:** `MK_CARRIERS=1` (single-threaded); `publish`'s one-CPU affinity.
- **Note on `std.http`:** Mk's `std.http.ReadRequest` operates over an async `Io`/`Reader` stream and `ParseHead` is internal, while C, Rust and C# have no standalone in-memory HTTP/1.1 request parser in std. Making the RFC 9112 parser itself the task allows a pure 6-language `std` comparison over identical memory buffers and rejection rules.
