// Why: sketch's package manifest, read by mk build, mk run and mk test.
// What: one console bin, `sketch`, and a per-target `plat` module.
// How: `src/plat` is chosen per target, so only the Windows build imports
// c.windows to post window messages; the Windows module is the one unsafe
// module.

const Package = pkg.Package(
    name: "sketch",
    bins: [pkg.Bin(name: "sketch", root: "src/main", subsystem: "console")],
    targets: [
        pkg.Select(os: "windows", modules: [pkg.Module(name: "plat", dir: "src/plat/windows")]),
        pkg.Select(os: "linux", modules: [pkg.Module(name: "plat", dir: "src/plat/linux")]),
        pkg.Select(os: "macos", modules: [pkg.Module(name: "plat", dir: "src/plat/linux")]),
    ],
    links: [
        pkg.Link(name: "libc.so.6", kind: "dynamic", path: "", target: "x86_64-linux"),
        pkg.Link(name: "libc.so.6", kind: "dynamic", path: "", target: "aarch64-linux"),
    ],
    // a module that `targets` selects is listed by its directory, not its name
    unsafe_modules: ["src/plat/windows"],
)
