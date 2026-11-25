// A run test of named consts as match patterns: same-module and
// qualified, alone, in alternatives, inside some(..) and a struct pattern.
const Package = pkg.Package(
    name: "constpattern",
    bins: [pkg.Bin(name: "constpattern", root: ".", subsystem: "console")],
)
