// verdict: reject E0101
// rule: R16
// note: `fuel` follows a `const` initializer or a `gen` line; a `var` global cannot state it (R16_accept_global_from_fuel_const)
Spin(): int {
    return 1
}

var Total: int = Spin() fuel 8_000_000 // <- E0101
