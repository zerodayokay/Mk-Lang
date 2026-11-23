// std.vm and runtime.AddFaultFilter, as a program sees them:
// a code region runs code written through CodeRegion.Edit; a fault filter
// resumes after a guard-page write; a filter resumes a fault in a
// code region at an edited pc; an unresumed fault in a code region is Mk's report. Unsafe only
// (E1101), so the program is a package whose app module is in unsafe_modules. coderegion.cases
// beside this directory.
const Package = pkg.Package(
    name: "coderegion",
    bins: [pkg.Bin(name: "coderegion", root: "app", subsystem: "console")],
    unsafe_modules: ["app"],
)
