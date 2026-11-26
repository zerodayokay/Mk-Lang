// Why: the panic texts are part of the report format, and the runtime must
// print exactly the compiler's names for its panic codes.
// What: RT-F8-PanicTextIsCodeName (the 17 panic texts) and RT-Site (a pc
// outside the image has no site), run by mk test over std.
// How: the driver test TestPanicTextsMatchPanicCode compares the texts with
// types.PanicCode.String(); this block pins the same 17 texts in the
// runtime itself.

import std.raw

test "RT-F8-PanicTextIsCodeName the 17 panic texts" {
    Assert(PanicText(1) == "overflow")
    Assert(PanicText(2) == "bounds")
    Assert(PanicText(3) == "divide by zero")
    Assert(PanicText(4) == "shift amount")
    Assert(PanicText(5) == "cast")
    Assert(PanicText(6) == "must")
    Assert(PanicText(7) == "assert")
    Assert(PanicText(8) == "explicit")
    Assert(PanicText(9) == "stack limit")
    Assert(PanicText(10) == "str boundary")
    Assert(PanicText(11) == "free non-empty")
    Assert(PanicText(12) == "wrong allocator")
    Assert(PanicText(13) == "enum tag")
    Assert(PanicText(14) == "contract")
    Assert(PanicText(15) == "unreachable")
    Assert(PanicText(16) == "suspend in callback")
    Assert(PanicText(17) == "unresolved function")
}

test "RT-Site a pc outside the executable image has no site" {
    // One Mk function at the image's base: inside the image its site is
    // found, above the image or below it there is none, though the row is
    // still the greatest addr <= pc.
    var site := Site(file: "r3.mk", line: 3, col: 1)
    lo: uint = 0x40_0000
    hi: uint = 0x50_0000
    unsafe "rows only compared, never called" {
        rows: [1]FuncRow = [FuncRow(addr: raw.FromAddr[u8](lo), site: raw.Of(&site), kind: 0)]
        Assert(raw.Same(SiteIn(rows[..], lo, hi, lo + 8, 0), raw.Of(&site)))
        Assert(raw.IsNull(SiteIn(rows[..], lo, hi, hi, 0)))
        Assert(raw.IsNull(SiteIn(rows[..], lo, hi, hi + 0x1000, 0)))
        Assert(raw.IsNull(SiteIn(rows[..], lo, hi, lo - 1, 0)))
        // No range yet (before Init): not bounded.
        Assert(raw.Same(SiteIn(rows[..], 0, 0, hi + 0x1000, 0), raw.Of(&site)))
    }
    // The process's own range, as Init recorded it, holds every row of the
    // function table, and a heap block lies outside it.
    plo := Trunc[uint](Rt.image_lo.Load())
    phi := Trunc[uint](Rt.image_hi.Load())
    Assert(plo != 0 && phi > plo)
    rows := FuncTable()
    for i in 0..rows.Len() {
        a := raw.Addr(rows[i].addr)
        Assert(a >= plo && a < phi)
    }
    p := HeapAlloc(64, 8)
    Assert(raw.IsNull(FaultSite(raw.Addr(p), 0)))
    HeapFree(p, 64, 8)
}
