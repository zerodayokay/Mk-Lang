export linear mut view struct O {
    arena: Arena
    n: int
}

export O.Clear(mut self) ends self {
    self.arena.Reset()
}
