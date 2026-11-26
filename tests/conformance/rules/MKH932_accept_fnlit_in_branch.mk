// verdict: ok
// rule: MKH-932
// note: a fn literal written inside `if T is u32` reads T as u32: its signature, a type written T in its body and a captured value of type T
Do(const f: fn(): int): int {
    return f()
}

Id[T](x: T): T {
    return x
}

Lit[T: Plain](x: T): T {
    if T is u32 {
        h := fn(y: T): T { return Id[T](y) + 1 }
        return h(x) + 1
    }
    return x
}

Cap[T: Plain](x: T): int {
    if T is u32 {
        return Do(fn(): int { return int(x) * 10 })
    }
    return -1
}

Run(): int {
    return int(Lit[u32](3)) + Cap[u32](4)
}
