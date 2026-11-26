// A run test of converting another module's signed consts to wider types:
// they are sign-extended, as a same-module const is.
const Package = pkg.Package(
    name: "constwiden",
    bins: [pkg.Bin(name: "constwiden", root: ".", subsystem: "console")],
)
