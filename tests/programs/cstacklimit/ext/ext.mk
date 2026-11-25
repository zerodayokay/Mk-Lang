// Why: the program's C function is reached through a bodiless extern, which
// is what runs on a carrier's system stack.
// What: Deep.
// How: one unsafe call.

extern deep_run(frames: u64): u64

/// Deep is lib/deep.c's deep_run(frames): 1 when its deepest frame lies below
/// the thread's StackLimit, else 0.
export Deep(frames: u64): u64 {
    unsafe "deep_run is the package's C; it reads its own frame and the TEB" {
        return deep_run(frames)
    }
}
