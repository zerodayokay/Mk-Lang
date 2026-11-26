// Why: callers of std.window need one open error set covering target
// support, closed windows, OS errors, out-of-memory and task cancellation.
// What: Error, Unsupported and Os.
// How: open error union of Unsupported, Os and the prelude errors Closed,
// OutOfMemory and Canceled.

/// Every error std.window returns.
export open type Error = Unsupported | Closed | Os | OutOfMemory | Canceled

/// This target has no window system std supports.
export error Unsupported

/// The OS refused: its error code.
export error Os(code: i64)
