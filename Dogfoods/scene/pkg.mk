// Why: scene is a portable 3D renderer and interactive viewer built on std.gpu,
// std.window, and std.image.
//
// What: the package manifest declaring the single console binary `scene` rooted
// at src/main with no unsafe modules or C sources.
//
// How: a plain pkg.Package value read by mk build and tools/dogfood.

const Package = pkg.Package(
    name: "scene",
    bins: [pkg.Bin(name: "scene", root: "src/main", subsystem: "console")],
)
