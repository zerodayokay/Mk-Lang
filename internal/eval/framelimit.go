// Why: a fixed frame limit makes deep but legitimate compile-time recursion
// fail, and new syntax to raise it would cost more than it buys; the fuel a
// const or gen line already states can raise it instead. A large const or
// global (a zeroed table of megabytes) is no recursion, so its own size
// raises it too.
//
// What: MaxStackLimit (256 MiB) and Machine.stackLimit.
//
// How: a segment's frames may hold the Evaluator's limit (1 MiB by
// default), or a quarter of the segment's stated fuel in bytes when that is
// more, or a declaration's room for its own value (segment.room) when that
// is more still, each capped at MaxStackLimit. A limit the Config states
// explicitly stands.

package eval

// MaxStackLimit is the largest frame limit fuel can raise: 256 MiB.
const MaxStackLimit = 256 << 20

// stackLimit is the frame-byte limit of segment s: the Evaluator's limit,
// or fuel/4 bytes, or s.room, whichever is most, the last two capped at
// MaxStackLimit.
func (m *Machine) stackLimit(s *segment) uint64 {
	lim := m.ev.stackLimit
	if lim != DefaultStackLimit {
		return lim // a limit the Evaluator's Config states (tests) stands
	}
	if q := min(s.fuel/4, MaxStackLimit); q > lim {
		lim = q
	}
	return max(lim, min(s.room, MaxStackLimit))
}
