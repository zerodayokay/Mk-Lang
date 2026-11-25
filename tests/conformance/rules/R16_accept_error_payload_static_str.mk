// verdict: ok
// rule: R16
// note: an error's str payload takes a literal, a const, a global, a result that comes from no parameter, and another error's static str, read directly or through a reference; an error set holding it formats
error Bad(msg: str, at: int)

error Other(code: int)

error Wrap(inner: (Bad | Other))

const Fixed: str = "fixed"

var Name: str = "global"

Pick(k: int): str {
    if k == 0 {
        return "zero"
    }
    return Fixed
}

Fail(k: int): Bad!void {
    match k {
        0 { fail Bad(msg: "literal", at: k) }
        1 { fail Bad(msg: Fixed, at: k) }
        2 { fail Bad(msg: Name, at: k) }
        _ { fail Bad(msg: Pick(k), at: k) }
    }
}

Rethrow(k: int): Bad!void {
    Fail(k) else e {
        var again := Bad(msg: "again", at: e.at)
        again.msg = e.msg
        fail again
    }
}

Peek(e: &Bad): str {
    return e.msg
}

Copy(e: &Bad): Bad!void {
    fail Bad(msg: e.msg, at: e.at + 1)
}

Show(e: &(Bad | Other), w: mut Writer): WriteError!void {
    try e.Format(w)
}
