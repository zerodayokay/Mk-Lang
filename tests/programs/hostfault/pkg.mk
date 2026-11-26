// runtime.AddFaultFilter on Windows sees a fault whose pc is in host code (a
// system DLL, a package's C source) on a stack Mk runs on, here a carrier's
// system stack during a user extern called from a task and a blocking-pool
// thread, and a fault no filter resumes still reaches the host's own
// __except. Unsafe only (E1101), so the program is a package whose app
// module is in unsafe_modules. hostfault.cases beside this directory;
// hostfaultmain is the same in a program with no task.
const Package = pkg.Package(
    name: "hostfault",
    bins: [pkg.Bin(name: "hostfault", root: "app", subsystem: "console")],
    c_sources: [pkg.CSource(path: "lib/host.c")],
    unsafe_modules: ["app"],
)
