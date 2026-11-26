// verdict: reject E0206
// note: a pattern binding cannot shadow a const generic
Pick[const N: int](x: int): int {
    return match x {
        N { 1 } // <- E0206
        _ { 0 }
    }
}
