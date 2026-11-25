// A function's const generic inferred from an array argument's length:
// the call names no bracket argument, and N comes from the argument's type,
// alone, beside a type parameter, and twice for an array of arrays.

Total[const N: int](xs: &[N]int): int {
    var sum := 0
    for i in 0..N {
        sum += xs[i]
    }
    return sum
}

Largest[T: Ord + Plain, const N: int](xs: [N]T): T {
    var best := xs[0]
    for x in xs {
        if best.Less(&x) {
            best = x
        }
    }
    return best
}

Cells[const R: int, const C: int](g: &[R][C]u8): int {
    _ = g
    return R * C
}

export Main(sys: &System): WriteError!void {
    xs := [1, 2, 3, 4]
    ys: [3]i64 = [7, 42, 9]
    g: [2][5]u8 = [[0; 5]; 2]
    try sys.Print("{a} {b} {c} {d}\n", (a: Total(&xs), b: Largest(ys), c: Cells(&g), d: Total[2](&[10, 20])))
}
