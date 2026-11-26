// verdict: reject E0507
// rule: R16
// note: a mut method on a shared sub-slice of a global is a mut borrow of the global: E0507 names it (`Table[0..2] is part of the global Table`) and offers no exclusive sub-slice, which is E0507 too
var Table: [8]u64 = [0; 8]

Clear() {
    Table[0..2].Fill(0) // <- E0507
}
