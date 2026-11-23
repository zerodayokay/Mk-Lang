// verdict: reject E0502
// note: a shared whole borrow of a split view: a view writes past a shared reference (R11), so a whole `&v`, here the `&self` of Mix, makes a split view whole, and v.a then holds n's loan: E0502 at the write to n. Mix moves b into a through raw storage, which only a view may do behind `&self`. With a shared borrow of a view keeping the split (mutant M-P4-view-a6), v.a keeps only t's loan, the program checks clean, and `mk run` prints `v=4242`: *v.a reads n through b's old loan after n was written while that loan was live. This package's pkg.mk lists `.` in unsafe_modules
import std.raw

mut view struct VTwo {
    a: &i32
    b: &i32
}

VTwo.Mix(&self) {
    unsafe "witness: a view writes past a shared reference" {
        raw.Write(raw.Of(&self.a), self.b)
    }
}

Inner(t: &i32): i32 {
    var n: i32 = 7
    var v := VTwo(a: t, b: t)
    v.b = &n
    v.Mix()
    n = 4242 // <- E0502
    return *v.a
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    t: i32 = 1
    v := Inner(&t)
    try Print(mut out, "v={v}\n", (v))
}
