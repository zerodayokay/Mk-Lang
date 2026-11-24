// verdict: reject E0605
// note: a copied `plain view struct` handle borrowing a local cannot be stored into a global Mutex even though it is Plain + Freeze
plain view struct Sym {
    id: u32
}

var GlobalSym: Mutex[?Sym] = Mutex[?Sym].New(none)

Intern(n: &i32): Sym from n {
    _ = n
    return Sym(id: 1)
}

Stash(io: Io) {
    local: i32 = 7
    s := Intern(&local)
    s2 := s
    var g := GlobalSym.Lock(io) else {
        return
    }
    g.Store(some(s2)) // <- E0605
    g.Unlock()
}

export Main(sys: &System): void {
    Stash(sys.io)
}
