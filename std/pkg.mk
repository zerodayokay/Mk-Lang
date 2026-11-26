// Why: std is built like any other package, from a manifest.
// What: Package, std's manifest: the per-target modules and the modules
// allowed unsafe code.
// How: each target OS selects its own sys, sys.lc and linux modules
// (sys/windows, sys/linux, sys/macos, free/sys, sys/nutshell; sys.lc is
// macOS's libSystem names, empty elsewhere); unsafe_modules lists the std modules
// that may hold unsafe blocks.

const Package = pkg.Package(
    name: "std",
    bins: [],
    deps: [],
    c_sources: [],
    c_imports: [],
    asm_sources: [],
    targets: [
        pkg.Select(os: "windows", modules: [pkg.Module(name: "sys", dir: "sys/windows"), pkg.Module(name: "linux", dir: "linux/other"), pkg.Module(name: "sys.lc", dir: "lc/other")]),
        pkg.Select(os: "linux", modules: [pkg.Module(name: "sys", dir: "sys/linux"), pkg.Module(name: "linux", dir: "linux/linux"), pkg.Module(name: "sys.lc", dir: "lc/other")]),
        pkg.Select(os: "freestanding", modules: [pkg.Module(name: "sys", dir: "free/sys"), pkg.Module(name: "linux", dir: "linux/other"), pkg.Module(name: "sys.lc", dir: "lc/other")]),
        pkg.Select(os: "macos", modules: [pkg.Module(name: "sys", dir: "sys/macos"), pkg.Module(name: "linux", dir: "linux/other"), pkg.Module(name: "sys.lc", dir: "lc/macos")]),
        pkg.Select(os: "nutshell", modules: [pkg.Module(name: "sys", dir: "sys/nutshell"), pkg.Module(name: "linux", dir: "linux/other"), pkg.Module(name: "sys.lc", dir: "lc/other")]),
    ],
    unsafe_modules: ["math", "core", "rt", "sync", "sys/windows", "sys/linux", "task", "coll", "mem", "sim", "dyn", "libc", "db/sqlite", "http", "simd", "simd/kernel", "free", "free/sys", "sys/nutshell", "sys/macos", "lc/macos", "x509", "runtime", "vm", "window"],
    links: [],
)
