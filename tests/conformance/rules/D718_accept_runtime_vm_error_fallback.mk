// verdict: ok
// rule: D-718
// note: callers can distinguish the current members of the runtime and VM error sets and cover future members with a fallback
import std.runtime
import std.vm

RuntimeKind(e: runtime.Error): int {
    return match e {
        runtime.Unsupported { 1 }
        runtime.OsError(code: _) { 2 }
        _ { 0 }
    }
}

VmKind(e: vm.Error): int {
    return match e {
        OutOfMemory { 1 }
        vm.Misaligned { 2 }
        vm.Unsupported { 3 }
        vm.Os(code: _) { 4 }
        _ { 0 }
    }
}
