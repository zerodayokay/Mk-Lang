// verdict: reject E0410
// note: a recursive struct holding a plain view over itself (List[Node]) beside Cell[?&i32] must not memoize List[Node].LoanInterior=false during the cycle; an exported view struct wrapping List[Node] must still require mut view (mutant M-D756-rec-view-low)
struct Node {
    next: List[Node]
    c: Cell[?&i32]
}

export view struct Bad { // <- E0410
    xs: List[Node]
}

export Main(sys: &System): void {
    _ = sys
}
