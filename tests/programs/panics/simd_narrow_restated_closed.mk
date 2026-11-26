// Why: A NarrowSat lane-count clause can depend on an open lane count.
// What: Exercise the entry contract in dev and the NarrowSat guard in release.
// How: Pass the input lane count through the generic.
import std.simd

N[const K: int](a: simd.Vec[u32, K], b: simd.Vec[u32, K]): int
    requires K == 2 || K == 4 || K == 8 || K == 16 || K == 32 || K == 64
    requires 8 == 2 * K && 8 <= 64 && 2 * SizeOf[u8]() == SizeOf[u32]()
{
    r := a.NarrowSat[u8, 8](b)
    return Cast[int](r.Lane(0))
}

export Main(sys: &System): int {
    n := Cast[u32](sys.args.Len())
    return N[4](simd.Vec[u32, 4].Splat(300 + n), simd.Vec[u32, 4].Splat(7))
}
