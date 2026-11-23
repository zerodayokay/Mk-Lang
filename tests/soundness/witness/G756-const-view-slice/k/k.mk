export mut view struct W[T] {
    q: ?&T
}

export W[T].Keep(&self, x: &T into self) {
    _ = self
    _ = x
}

MakeW(a: &Arena): &W[i32] from a {
    w := a.Put(W[i32](q: none)) else {
        return MakeW(a)
    }
    return &*w
}

export struct Box {
    export w: &W[i32]
}

export const GS: []Box = [Box(w: MakeW(EvalArena()))]
