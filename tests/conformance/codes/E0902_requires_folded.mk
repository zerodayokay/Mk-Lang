// verdict: ok
// note: a requires clause that holds for every run of an instance (here N == 16 over the const generic alone) is not checked again on entry, in any profile: a thousand calls of WithClause[16] fit a fuel that leaves about 6 steps a call over the same loop of NoClause[16], and a check of the clause on each entry needs about 33 more
WithClause[const N: int](x: int): int
    requires N == 16 || N == 32 || N == 64
{
    return x + N
}

NoClause[const N: int](x: int): int {
    return x + N
}

LoopWith(): int {
    var s := 0
    for i in 0..1000 {
        s += WithClause[16](i)
    }
    return s
}

LoopNo(): int {
    var s := 0
    for i in 0..1000 {
        s += NoClause[16](i)
    }
    return s
}

const A: int = LoopWith() fuel 40_000

const B: int = LoopNo() fuel 40_000
