// Why: the driver must know std's shape before it reads std's modules: which
// module is the prelude, which modules only std may import, which
// directories are no module, and which modules exist for one target only.
//
// What: preludeModule, preludeNames, stdInternal (isStdInternal),
// section17Modules, the stdLayout table, stdTargetOnly and stdTargetRefused.
//
// How: plain tables. PreludeModule, PreludeNames and Internal are package
// std's own exports; the driver reads them from there and keeps no copy.

package driver

import (
	"slices"

	"mk/std"
)

// preludeModule is std.PreludeModule: the module whose PreludeNames every
// module sees with no import.
const preludeModule = std.PreludeModule

// preludeNames is std.PreludeNames, the language reference's prelude list in
// its order.
var preludeNames = std.PreludeNames[:]

// stdInternal is std.Internal: modules only std modules may import (E0202
// reason std-internal), and std.simd.kernel, the std-only module of the
// crypto and text vector kernels, and std.sys.lc, macOS's libSystem names, until
// std.Internal names them too.
var stdInternal = withModule(withModule(std.Internal[:], "std.simd.kernel"), "std.sys.lc")

// withModule is mods with m appended unless it is already there, in a new
// slice.
func withModule(mods []string, m string) []string {
	out := slices.Clone(mods)
	if !slices.Contains(out, m) {
		out = append(out, m)
	}
	return out
}

// isStdInternal reports whether path is a module of stdInternal.
func isStdInternal(path string) bool { return slices.Contains(stdInternal, path) }

// section17Modules are the import paths of the language reference's std
// module table.
// An import of one that this std does not provide is E0202 reason
// not-implemented, with its row named in the message.
var section17Modules = []string{"std.atomic", "std.big", "std.build", "std.cli", "std.coll", "std.compress", "std.crypto",
	"std.csv", "std.db.sqlite", "std.derive", "std.dl", "std.dyn", "std.encoding", "std.encoding.cjk", "std.fs", "std.hash", "std.http",
	"std.json", "std.linux", "std.log", "std.math", "std.mem", "std.net", "std.proc", "std.quic", "std.regex", "std.runtime", "std.sim", "std.sync",
	"std.term", "std.testing", "std.text", "std.time", "std.time.tzdata", "std.tls", "std.url", "std.vm", "std.websocket", "std.x509"}

// stdLayout is the fixed part of std's module table:
// the directories that are never a module under their own path.
var stdLayout = struct {
	// NotModules are std directories that form no module: corpus/ holds the
	// test prelude (the corpus reads it), pkg/ is std.pkg, reached only by
	// pkg.mk's implicit binding.
	NotModules []string
	// TargetDirs are the per-OS directories std/pkg.mk's targets select;
	// they never form a module under their own path, even when std/pkg.mk
	// is missing (then std.sys is not formed at all).
	TargetDirs []string
	// TestPrelude is std.TestPreludePath.
	TestPrelude string
	// Manifest is std's own manifest file.
	Manifest string
}{
	NotModules:  []string{"corpus", "pkg"},
	TargetDirs:  []string{"free/sys", "linux/linux", "linux/other", "lc/macos", "lc/other", "sys/linux", "sys/macos", "sys/nutshell", "sys/windows"},
	TestPrelude: std.TestPreludePath,
	Manifest:    "pkg.mk",
}

// stdTargetOnly are the std modules that exist for one target only, by
// import path, with that target's name: std/pkg.mk's targets select
// std/linux/linux as std.linux for os "linux" and the empty std/linux/other
// elsewhere (a selection maps the same names on every target), and on any
// other target, aarch64-linux
// included, its import is E0202 reason target-selected at the import, with a
// message naming the target.
var stdTargetOnly = map[string]string{"std.linux": "x86_64-linux"}

// stdTargetRefused reports whether path is a target-only std module that
// target, by name, does not have.
func stdTargetRefused(path, target string) bool {
	want, ok := stdTargetOnly[path]
	return ok && want != target
}
