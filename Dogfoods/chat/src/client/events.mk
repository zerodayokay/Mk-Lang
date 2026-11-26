// Why: a replay needs to keep receiving HTTPS events while it runs other
// clients' commands.
// What: ReadEvents, the task that reads one web response.
// How: the task owns the HTTP exchange and writes its head and decoded body
// to a pipe. The replay reads the pipe like its other line-oriented inputs.

import std.http
import std.net
import std.proc
import std.time
import std.url

ReadEvents(h: &Heap, io: Io, client: &http.Client, addr: str, path: str, pipe: proc.PipeWriter) {
    defer pipe.Close()
    var a := Arena.New(h) else { return }
    defer a.Free()
    var buf: [4096]u8 = [0; 4096]
    var w := pipe.Writer(io, mut buf[..])
    StreamEvents(&a, io, client, addr, path, mut w) else e {
        Print(mut w, "event stream failed: {e}\n\n", (e)) else {}
        w.Flush() else {}
    }
}

StreamEvents(a: &Arena, io: Io, client: &http.Client, addr: str, path: str, w: mut Writer): http.Error|url.Error!void {
    u := try url.Parse(a, try a.Fmt("https://{addr}{path}", (addr, path)), none)
    var x := try OpenEvents(a, io, client, &u)
    onerr x.Abandon()
    version := if x.Version() == http.HttpVersion.Http2 { "HTTP/2" } else { "HTTP/1.1" }
    try Print(w, "{version} {status}\n", (version, status: x.status))
    for field in x.headers {
        if !field.name.StartsWith(":") {
            try Print(w, "{field.name}: {field.value}\n", (field))
        }
    }
    try w.Str("\n")
    try w.Flush()
    var empty: [0]u8 = []
    var body := x.Body(mut empty[..])
    var piece: [4096]u8 = [0; 4096]
    for {
        n := try body.Read(mut piece[..])
        if n == 0 { break }
        try w.Bytes(piece[..n])
        try w.Flush()
    }
    try x.Close(io)
}

// The replay task can be scheduled before the server binds its socket.
OpenEvents(a: &Arena, io: Io, client: &http.Client, u: &url.Url): http.Error!http.Exchange from a, client {
    headers: [0]http.Header = []
    req := http.ClientRequest(method: "GET", url: u, headers: headers[..], body: b"")
    scope attempt := io.Scope() {
        attempt.CancelAfter(time.Seconds(5))
        for {
            x := client.Do(a, io, &req) else e {
                match e {
                    net.Refused {
                        try io.Sleep(time.Millis(10))
                        continue
                    }
                    _ { fail e }
                }
            }
            return x
        }
    }
}
