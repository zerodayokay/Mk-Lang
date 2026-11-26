// Why: the mk binary must carry the whole standard library and runtime, so
// a program builds with nothing installed beside it.
//
// What: FS (every std source, the manifest and the import-library
// definitions), the prelude's module and names (PreludeModule,
// PreludeNames), the std-internal modules (Internal) and TestPreludePath.
//
// How: one go:embed pattern per directory depth. The file holds data only,
// so the Go tests beside the Mk sources stay out of FS and editing them does
// not invalidate the Go test cache of every package that embeds std.

// Package std embeds the Mk standard library, including the runtime, so that
// mk is self-contained. It holds data only: no parser, no
// checker, no import of any internal package (archtest: std imports nothing).
package std

import "embed"

// FS holds every file under std/, at slash-separated paths relative to std/
// ("core/core_str.mk", "pkg.mk", "corpus/testprelude.mk"). A directory d
// holding .mk files is the module "std." + d with "/" replaced by ".", except
// the per-target directories that pkg.mk's targets map to one module: for the
// build's target OS, sys/windows or sys/linux is the module "std.sys", and the
// other one is not loaded. corpus/ is not
// a module. No file name under std/ begins with "." or "_".
//
// Only the Mk sources, the manifest and the import-library definitions are
// embedded, never the Go tests beside them, so a test edit does not change
// FS and invalidate the Go test cache of every package that embeds std. A
// module directory
// deeper than std/<a>/<b>/ needs a pattern here.
//
//go:embed pkg.mk */*.mk */*/*.mk */*/*.def
var FS embed.FS

// PreludeModule is the module whose 65 PreludeNames every module sees with no
// import. Error identities hash module paths, so this path
// and the module of every std error never change.
const PreludeModule = "std.core"

// PreludeNames is the language reference's prelude list, in its order. Exactly these
// names of PreludeModule are visible without import; its other exports are
// std-internal. "Path" is listed and not declared: it is reserved.
var PreludeNames = [65]string{"Alloc", "Arch", "Arena", "Block", "Box", "Canceled",
	"Cell", "Chan", "Chunks", "ChunksMut", "Closed", "Code", "Eq", "Field", "Flow", "FlowKind", "Format",
	"Free", "Gen", "Guard", "Hash", "HashKey", "Hasher", "Heap", "HeapList", "HeapMap",
	"HeapText", "Io", "Limits", "Lines", "List", "Map", "MapIter", "Method", "Mutex",
	"Ord", "Os", "OutOfMemory", "Param", "ParseError", "Path", "Print", "ReadError",
	"Reader", "Receiver", "Replace", "Scalars", "Scope", "Set", "Slice", "Split",
	"StackSize", "Swap", "System", "TargetInfo", "Text", "TextMap", "TextMapIter", "Type",
	"TypeKind", "Utf8Error", "Variant", "Words", "WriteError", "Writer"}

// Internal lists the std modules that only std modules may import. An import
// of one from outside std is E0202 with the fact reason: std-internal.
var Internal = [...]string{"std.core", "std.raw", "std.rt", "std.sys", "std.target", "std.task", "std.http.fields", "std.http.h2", "std.http.h3", "std.net.pipe", "std.sync.lead"}

// TestPreludePath is the FS path of the corpus test prelude.
// It is not a module: only the corpus runner reads it, and mk build, run,
// check and test of a user package never see it.
const TestPreludePath = "corpus/testprelude.mk"
