// verdict: reject E0901
// note: a requires clause over a const generic that only the entry check judges (its argument is a SizeOf no clause can spell) still runs on entry when it is false for the instance: Lane[128] panics with contract at compile time
Lane[const N: int](): int
    requires N < 64
{
    return N
}

G[U](): int {
    return Lane[SizeOf[[2]U]()]()
}

const Ok: int = G[[16]u8]()

const K: int = G[[64]u8]() // <- E0901
