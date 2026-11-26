// Why: a package's manifest, pkg.mk, is Mk code; these are the types it is
// written in, so the compiler checks a manifest as it checks any program.
// What: Package, Bin, Dep, CSource, CImport, AsmSource, Select, Module and
// Link.
// How: reached only through a manifest's implicit `pkg` binding. Every field
// is named, except that a manifest may leave out one holding "", [] or
// false; newer fields go at the end of each struct. List fields are []T,
// written in a manifest as static slices. It imports nothing and writes no
// unsafe.

/// The package: its name, its bins, and the build description.
export struct Package {
    export name: str
    export bins: []Bin
    export deps: []Dep
    export c_sources: []CSource
    export c_imports: []CImport
    export asm_sources: []AsmSource
    export targets: []Select
    export unsafe_modules: []str
    export links: []Link
    /// The oldest mk that builds this package: "0.1". "" for none.
    export mk: str
    /// Dependencies whose names only test blocks may use.
    export test_deps: []Dep
    /// Fetched packages granted unsafe code, assembly, C and libraries: each a
    /// source, or a direct dependency's local name.
    export unsafe_deps: []str
    /// Member package directories; read in the root manifest only.
    export members: []str
    /// Sources replaced in the whole build; read in the root manifest only.
    export replace: []Dep
}

/// A bin: its name, the package-relative directory of its root module, its
/// linker script (a package-relative file, x86_64-freestanding only, else
/// "") and its cpu: "" for the target's default, "general-regs-only"
/// (freestanding only) or "baseline", and its subsystem: "console", or "windows" for an
/// x86_64-windows program linked to open no console window.
export struct Bin {
    export name: str
    export root: str
    export link_script: str
    export cpu: str
    export subsystem: str
    /// "" (an executable), "dynamic" or "static".
    export kind: str
    /// The library's ABI number for its soname and version node; "" is "1".
    export abi: str
}

/// A dependency on another package: exactly one of path (a directory relative to the manifest
/// that names it), url (a .tar.gz, .tar or .zip archive, with hash
/// "sha256:<64 hex digits>") and source (a git repository, host/path, at
/// the tag v<version> or a higher one another manifest asks for).
export struct Dep {
    export name: str
    export path: str
    export url: str
    export hash: str
    /// A repository address, host/path, fetched over git.
    export source: str
    /// The oldest version accepted, X.Y.Z.
    export version: str
}

/// A C source file of the package, compiled by Clang for the target and
/// linked against std.libc, never a C runtime.
export struct CSource {
    export path: str
    export flags: []str
}

/// A C header import, `import c.<name>`: its headers, the
/// defines they are read with, the prototypes of the function-like macros
/// to bind ("int MAX(int a, int b)"), whether the per-target bindings
/// under gen/<target>/c/ are read when a target's headers are absent, and
/// the package-relative directories searched for headers after the package
/// root (by the translation, the shim and the package's C sources alike),
/// and whether the headers are read with the host's SDK (sdk: true).
export struct CImport {
    export name: str
    export headers: []str
    export defines: []str
    export macros: []str
    export vendor: bool
    export include_dirs: []str
    export sdk: bool
}

/// An assembly source file (.s, or .S through the preprocessor), assembled
/// for target ("" for every target); its symbols are extern.
export struct AsmSource {
    export path: str
    export target: str
}

/// One target OS's module directories: each name an import path, each dir
/// package-relative.
export struct Select {
    export os: str
    export modules: []Module
}

/// One selected module: its import name and its directory.
export struct Module {
    export name: str
    export dir: str
}

/// A library the package's extern functions resolve to, for one target:
/// kind "dynamic", which the OS loader opens
/// at start, "lazy", loaded on the first call of one of its functions
/// (x86_64-windows only), or "static", an archive linked in; target "x86_64-windows",
/// "x86_64-linux" or "aarch64-linux"; a dynamic or lazy name is the loader's exact file name; path is
/// package-relative: "", a .def name list, the .dll, or the .lib or .a.
export struct Link {
    export name: str
    export kind: str
    export path: str
    export target: str
}
