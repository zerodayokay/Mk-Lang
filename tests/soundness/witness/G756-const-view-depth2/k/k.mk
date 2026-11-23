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

export struct Box2 {
    export b: &Box
}

const GB: Box = Box(w: MakeW(EvalArena()))

export const GB2: Box2 = Box2(b: &GB)
