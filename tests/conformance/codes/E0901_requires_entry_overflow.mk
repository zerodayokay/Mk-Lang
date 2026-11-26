// verdict: reject E0901
// note: an entry-checked requires clause whose arithmetic overflows for the instance still runs on entry, and the overflow is its panic
Over[const N: int](): int
    requires N * 4611686018427387904 >= 0
{
    return N
}

GO[U](): int {
    return Over[SizeOf[[2]U]()]()
}

const Ok: int = GO[[0]u8]()

const K: int = GO[[8]u8]() // <- E0901
