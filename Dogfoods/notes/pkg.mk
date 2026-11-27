// Why: notes links C code: the sqlite amalgamation and its own normtext.c, so
// the package says how to build them.
//
// What: The package description: the bin `notes`, the two C sources with their
// flags, the C imports of their headers, and the one unsafe module,
// src/store/cext.
//
// How: Build the bundled SQLite amalgamation with the flags std.db.sqlite requires.

const Package = pkg.Package(
    name: "notes",
    bins: [pkg.Bin(name: "notes", root: "src/main", link_script: "", cpu: "", subsystem: "console")],
    deps: [],
    c_sources: [
        pkg.CSource(path: "csrc/sqlite/sqlite3.c", flags: ["-DSQLITE_OS_OTHER=1", "-DSQLITE_THREADSAFE=0", "-DSQLITE_OMIT_LOCALTIME", "-DSQLITE_DEFAULT_MEMSTATUS=0", "-DSQLITE_TEMP_STORE=3", "-DSQLITE_WITHOUT_MSIZE", "-DSQLITE_DISABLE_INTRINSIC", "-DSQLITE_OMIT_SEH"]),
        pkg.CSource(path: "csrc/normtext.c", flags: []),
    ],
    c_imports: [
        pkg.CImport(name: "sqlite3", headers: ["csrc/sqlite/sqlite3.h"], defines: [], macros: [], vendor: false),
        pkg.CImport(name: "normtext", headers: ["csrc/normtext.h"], defines: [], macros: [], vendor: false),
    ],
    asm_sources: [],
    targets: [],
    unsafe_modules: ["src/store/cext"],
    links: [],
)
