// verdict: reject E0507
// rule: R16
// note: a global is never assigned, an element included: E0507 names the global (`Table[_] is part of the global Table`); a table that changes holds atomics (EV526_accept_global_shared)
var Table: [8]u64 = [0; 8]

Set() {
    Table[7] = 42 // <- E0507
}
