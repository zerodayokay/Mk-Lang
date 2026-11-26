// verdict: reject E0707
// Source's Get names no error set, so no impl of it may fail: at
// Source[Bad!int] its result T is an E!T the method returns as a value.
error Bad

interface Source[T] {
    Get(&self): T
}

struct Cell {
    n: int
}

impl Source[Bad!int] for Cell {
    Get(&self): Bad!int { // <- E0707
        if self.n < 0 {
            fail Bad
        }
        return self.n
    }
}
