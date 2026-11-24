// verdict: reject E0505
// note: a copy of a `plain view struct` still carries its source container's loan, so letting that container die fails E0505
plain view struct Sym {
    id: u32
}

struct Table {
    tag: u32
}

Intern(t: &Table): Sym from t {
    _ = t
    return Sym(id: 1)
}

Read(s: Sym): u32 {
    return s.id
}

Bad(): u32 {
    var copy: ?Sym = none
    {
        t := Table(tag: 1)
        h := Intern(&t) // <- E0505
        copy = some(h)
    }
    if s := copy {
        return Read(s)
    }
    return 0
}

export Main(sys: &System): void {
    _ = Bad()
}
