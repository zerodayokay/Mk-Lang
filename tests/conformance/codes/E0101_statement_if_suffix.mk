// verdict: reject E0101
// note: an if that starts a statement is the statement, not a value; a suffix after its closing brace is E0101
Pick(c: bool, a: str, b: str) {
    if c { a } else { b }.Len() // <- E0101
}
