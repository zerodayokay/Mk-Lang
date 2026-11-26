// verdict: reject E0706
// note: std.simd's NarrowSat inside `if U is u8` in a generic over a bare lane type parameter: U written in the branch is u8, so the clause 2 * SizeOf[U]() == SizeOf[T]() is judged at u8 and u32, where it is false, as for NarrowSat[u8, 8]
import std.simd

N[U: simd.Lane](a: simd.Vec[u32, 4], b: simd.Vec[u32, 4]): int {
    if U is u8 {
        r := a.NarrowSat[U, 8](b) // <- E0706
        return Cast[int](r.Lane(0))
    }
    return -1
}

export Main(sys: &System): int {
    n := Cast[u32](sys.args.Len())
    return N[u8](simd.Vec[u32, 4].Splat(300 + n), simd.Vec[u32, 4].Splat(7))
}
