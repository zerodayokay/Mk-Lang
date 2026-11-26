// *void, C's untyped pointer, built from a typed pointer, a null and an
// address through std.raw with `void` as a written type argument. Unsafe
// only (E1101), so the program is a package whose app module is in
// unsafe_modules. voidptr.cases beside this directory.
const Package = pkg.Package(
    name: "voidptr",
    bins: [pkg.Bin(name: "voidptr", root: "app", subsystem: "console")],
    unsafe_modules: ["app"],
)
