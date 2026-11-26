// verdict: reject E0605
// rule: R16
// note: a payload str viewing a local buffer, assigned to the field after the error is built
error Bad(msg: str)

Decode(): Bad!void {
    buf: [2]u8 = [104, 105]
    s := str.From(buf[..]) else { "" }
    var e := Bad(msg: "none")
    e.msg = s // <- E0605
    fail e
}
