// Why: mk needs a manifest to build the offline relay demonstration.
// What: one console program named relay.
// How: the program uses only the standard library.

const Package = pkg.Package(
    name: "relay",
    bins: [pkg.Bin(name: "relay", root: "src/main", subsystem: "console")],
)
