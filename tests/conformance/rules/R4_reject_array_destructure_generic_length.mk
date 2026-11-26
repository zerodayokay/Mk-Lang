// verdict: reject E0301
// rule: R4
// note: an array destructure needs the array's length where it is checked
Generic[const N: int](rs: [N]Res) {
    [a, b] := rs // <- E0301
    a.Close()
    b.Close()
}
