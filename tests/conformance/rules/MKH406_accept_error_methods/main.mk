// verdict: ok
// rule: MKH-406
// note: an error declares methods as a struct does: self is the error's value, by shared or exclusive borrow or by value (a copy, since a payload is plain); a payload-free error takes them too, another module calls an exported one, and a caught error calls them
import errs

error Timeout

error Code(n: int)

Timeout.Millis(&self): int {
    return 30
}

Code.Take(self): int {
    return self.n * 10
}

Code.Again(self): Code!int {
    fail self
}

Work(n: int): errs.Bad!int {
    if n > 0 {
        fail errs.Bad.New(n)
    }
    return 0
}

Run(): int {
    var e := errs.Bad.New(1)
    e.Bump(4)
    k := e.Twice() // 105
    f := errs.Bad.Twice
    g := f(&e) // 105
    t := Timeout
    c := Code(n: 2)
    v := c.Take() // 20, and c is still usable: a payload is plain
    w := Work(3) else x { x.Twice() } // 103
    r := c.Again() else x { x.n } // 2
    return k + g + t.Millis() + v + w + r
}
