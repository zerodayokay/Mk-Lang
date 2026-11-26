export error Bad(code: int)

export Bad.New(code: int): Bad {
    return Bad(code: code)
}

export Bad.Twice(&self): int {
    return self.code + 100
}

export Bad.Bump(mut self, by: int) {
    self.code += by
}
