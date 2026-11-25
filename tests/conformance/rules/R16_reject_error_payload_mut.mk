// verdict: reject E0605
// rule: R16
// note: a mut borrow of an error's str payload could store any str through it
error Bad(msg: str)

Set(p: mut str, v: str into p) {
    *p = v
}

Rename(s: str): Bad!void {
    var e := Bad(msg: "x")
    Set(mut e.msg, s) // <- E0605
    fail e
}
