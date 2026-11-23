// verdict: reject E0507
// note: a store clause `*q into p` cannot target a shared reference to Cell[i32], Mutex[i32], Chan[i32], atomic.Spin[i32], or a plain read-only view struct, because none of them can take a loan store through &
import std.atomic

view struct ReadOnly {
    s: str
}

PutCell(p: &Cell[i32], q: &&i32) *q into p { // <- E0507
    _ = p
    _ = q
}

PutMutex(p: &Mutex[i32], q: &&i32) *q into p { // <- E0507
    _ = p
    _ = q
}

PutChan(p: &Chan[i32], q: &&i32) *q into p { // <- E0507
    _ = p
    _ = q
}

PutSpin(p: &atomic.Spin[i32], q: &&i32) *q into p { // <- E0507
    _ = p
    _ = q
}

PutView(p: &ReadOnly, q: &&i32) *q into p { // <- E0507
    _ = p
    _ = q
}

export Main(sys: &System): void {
}
