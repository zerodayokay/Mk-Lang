// verdict: reject E0901
// note: an entry-checked requires clause is judged by the value of its operands in the instance's types: N - 200 is -184 for N = 16, so the signed comparison is false and the clause panics on entry
Signed[const N: int](): int
    requires N - 200 > 0
{
    return N
}

GS[U](): int {
    return Signed[SizeOf[[2]U]()]()
}

const Ok: int = GS[[128]u8]()

const K: int = GS[[8]u8]() // <- E0901
