// verdict: reject E0306
// rule: D-718
// note: the runtime and VM error sets may grow, so spelling every current member does not exhaust them outside std
import std.runtime
import std.vm

RuntimeKind(e: runtime.Error): int {
    return match e { // <- E0306
        runtime.Unsupported { 1 }
        runtime.OsError(code: _) { 2 }
    }
}

VmKind(e: vm.Error): int {
    return match e { // <- E0306
        OutOfMemory { 1 }
        vm.Misaligned { 2 }
        vm.Unsupported { 3 }
        vm.Os(code: _) { 4 }
    }
}
