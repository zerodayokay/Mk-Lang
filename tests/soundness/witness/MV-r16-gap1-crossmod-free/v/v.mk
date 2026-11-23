import std.raw

export var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

export mut view struct V {
    p: *Mutex[?&i32]
}

export V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

export V.Ptr(&self): *Mutex[?&i32] {
    return self.p
}
