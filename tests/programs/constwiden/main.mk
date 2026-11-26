// Why: a conversion of another module's const to a wider integer type is
// folded at compile time; a signed one must be sign-extended, as the module's
// own const and a local copy of the imported one are.
// What: Main prints each conversion, its own const's first.
// How: i64 and int of i32, i8 and i16 consts of this module and of consts,
// directly, through a local and in a const initializer.

import consts

const Own: i32 = -4
const Folded: i64 = i64(consts.C)

export Main(sys: &System): WriteError!void {
    own := i64(Own)
    local := consts.C
    through_local := i64(local)
    direct := i64(consts.C)
    as_int := int(consts.C)
    b := i64(consts.B)
    h := i32(consts.H)
    try sys.Print("{own} {through_local} {direct} {as_int} {Folded} {b} {h}\n", (own, through_local, direct, as_int, Folded, b, h))
}
