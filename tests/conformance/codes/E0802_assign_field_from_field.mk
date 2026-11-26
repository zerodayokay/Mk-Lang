// verdict: reject E0802
// note: a stored E!T read out of one field and assigned into another is E0802: the field form is the same store
error Bad

struct H {
    r: Bad!int
}

Use(a: &H, b: mut H) {
    b.r = a.r // <- E0802
}
