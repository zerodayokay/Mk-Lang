// hostfault in a program with no task: its Main runs on the main thread's
// own stack, where std installs no stack test, and runtime.AddFaultFilter
// still sees a fault whose pc is in a system DLL (MKH-493).
// hostfaultmain.cases beside this directory.
const Package = pkg.Package(
    name: "hostfaultmain",
    bins: [pkg.Bin(name: "hostfaultmain", root: "app", subsystem: "console")],
    unsafe_modules: ["app"],
)
