// Why: JSON APIs read a request body into a struct and answer with one; the
// struct's `gen json.Codec` already knows the JSON, so web only connects it
// to http.Request and http.Response.
//
// What: `gen web.Body(T)`, which writes T.FromRequest, and Reply, which
// writes a value with its codec as the response.
//
// How: Body is a generator rather than a generic function: a generic
// `Decode[T]` taking `T.FromJson` as a `fn` value cannot state that its
// result comes from the arena (the checker reads a `fn` value's generic
// result as global, E0602), while the concrete function the generator emits
// states `from a` as a handwritten one does.

import std.http
import std.json

/// Emits, for the struct t, which has a `gen json.Codec(t)` line,
/// `T.FromRequest(a: &Arena, req: &http.Request): http.Error!T from a`:
/// the request's body read as JSON into T. A body that is not UTF-8, not
/// JSON or not a T fails with Status(400), which http.Serve answers.
/// json.Parse keeps views of its text in its arena and a handler
/// lets no loan of req into a, so the parse runs in a scratch arena freed
/// before the return, and FromJson copies T's strings into a.
/// O(body length). Runs at compile time.
export Body(g: mut Gen, t: Type) {
    if t.Kind() != TypeKind.Struct {
        g.FailOn(t, "web.Body: only a struct with a json.Codec can be read from a body")
        return
    }
    pub := if t.Exported() { quote { export } } else { quote {} } // export as T is
    g.Emit(quote {
        $pub $(t.Name()).FromRequest(a: &Arena, req: &http.Request): http.Error!$(t.Name()) from a {
            text := str.From(req.body) else {
                fail http.Status(code: 400)
            }
            var scratch := try a.Sub()
            defer scratch.Free()
            v := json.Parse(&scratch, text) else {
                fail http.Status(code: 400)
            }
            x := $(t.Name()).FromJson(a, &v) else {
                fail http.Status(code: 400)
            }
            return x
        }
    })
}

/// Answers with status code, `Content-Type: application/json` and v
/// written by its codec's FormatJson. O(the JSON's length).
export Reply[T: json.Json](res: mut http.Response, code: int, v: &T): http.Error!void {
    res.Status(code)
    try res.Header("Content-Type", "application/json")
    var w := res.Writer()
    try v.FormatJson(mut w)
}
