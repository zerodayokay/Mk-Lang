// verdict: reject E0604
// rule: D-456
// note: calling a method that declares `*self into self` on a mut view parameter requires `*res into res` on the caller; without it each call is E0604 (for read-only view `http.Response`, see D456_accept_handler_without_clause)
mut view struct Sink {
    n: int
}

Sink.Header(mut self, name: str, value: str): void *self into self {
    _ = name
    _ = value
}

Sink.Body(mut self, data: []u8): void *self into self {
    _ = data
}

Reply(res: mut Sink) {
    res.Header("content-type", "text/plain") // <- E0604
    res.Body(b"ok\n")                        // <- E0604
}

export Main(sys: &System): void {
}
