export Put[T](t: &T, io: Io, x: &i32 into t, f: fn(u: &T, io: Io, y: &i32 into u)) {
    f(t, io, x)
}
