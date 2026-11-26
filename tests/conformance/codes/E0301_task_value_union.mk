// verdict: reject E0301
// Copy cannot fail: at T = Bad!void it returns the Bad!void as a value, so
// a task of it would never fail, and the scope would drop the error.
error Bad

Copy[T: Plain](x: &T): T {
    return *x
}

Run(io: Io, b: &(Bad!void)): Bad!void {
    try scope s := io.ScopeOf[Bad]() {
        s.go Copy(b) // <- E0301
    }
}
