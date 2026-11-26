// verdict: reject E0306
// note: an error's pattern on the left of := is match-only, as a variant's is: read the error's fields or take it apart with match
error Bad(code: int)

Code(e: Bad): int {
    Bad(code: c) := e // <- E0306
    return c
}
