// verdict: reject E0707
// Source's Get names an error set, so every impl of it must fail with it:
// a result written through an alias of Bad!int names none, so this Get
// returns the Bad!int as a value and cannot fail.
error Bad

type R = Bad!int

interface Source {
    Get(&self): Bad!int
}

struct Cell {
    n: int
}

impl Source for Cell {
    Get(&self): R { // <- E0707
        var r: R = Make(self.n)
        return r
    }
}

Make(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}
