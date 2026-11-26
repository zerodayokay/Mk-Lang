// Two deep C calls from one carrier: the second must run under the
// StackLimit the first left, not the one the carrier's thread started with.
// The cases are in cstacklimit.cases beside this directory.
const Package = pkg.Package(
    name: "cstacklimit",
    bins: [pkg.Bin(name: "cstacklimit", root: "app", subsystem: "console")],
    c_sources: [pkg.CSource(path: "lib/deep.c", flags: [])],
    unsafe_modules: ["ext"],
)
