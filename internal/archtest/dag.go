// Why: the compiler can be built, tested and changed in parts only while its
// packages form a strict hierarchy; one stray import can tie the front end to
// the back end.
//
// What: the package DAG as data (which package may import which), the extra
// imports only test files may make, and the size limit for Go files.
//
// How: TestPackageDAG walks every .go file of the module, reads its imports
// and fails on an edge this table does not list. The table is the single
// source of truth: change it in the same commit as the import it allows.

// Package archtest holds the compiler's package DAG as data and the test that
// enforces it over every .go file of the module. It imports nothing internal
// and nothing imports it.
package archtest

// Edge lists the packages one package may import directly. Names are paths
// relative to the module root with the "internal/" prefix dropped: "syntax"
// is mk/internal/syntax, "cmd/mk" is mk/cmd/mk, "std" is mk/std.
type Edge struct {
	Pkg     string
	Imports []string
}

// below lists every internal package the driver may import: everything but
// itself, the packages above it (corpus, stdtest, lsp, repl and cmd/mk),
// archtest and the tools.
var below = []string{"cimport", "sysview", "fetch", "git", "release", "format", "toolchain", "cache", "iface", "codegen", "pgo", "llvm", "abi", "mono", "gen",
	"eval", "borrow", "lower", "check", "fmtspec", "resolve", "syntax", "mir", "types", "ast", "diag", "source", "std"}

// DAG is the package hierarchy, row by row. A package missing here, or an
// import edge not listed, fails TestPackageDAG. Change this table in the
// same commit as the import it allows.
var DAG = []Edge{
	{"cmd/mk", []string{"driver", "corpus", "repl", "lsp"}},
	{"lsp", []string{"driver", "format", "diag", "source", "types", "iface"}},
	{"repl", []string{"driver", "eval", "mir", "types", "diag", "source"}},
	{"corpus", []string{"driver", "diag", "source"}},
	{"stdtest", []string{"driver", "check", "resolve", "syntax", "ast", "types", "diag", "source", "std"}},
	{"driver", below},
	{"cimport", []string{"sysview", "toolchain", "cache", "types", "diag", "source"}},
	{"sysview", nil}, // the system view archives: standard library only
	{"fetch", []string{"git", "cache", "diag", "source"}},
	{"git", []string{"source"}},     // git sources for the package fetcher
	{"release", []string{"source"}}, // release archives for mk upgrade
	{"format", []string{"syntax", "ast", "source"}},
	{"toolchain", []string{"source"}},
	{"cache", []string{"source"}},
	{"iface", []string{"mir", "types", "diag", "source"}},
	{"codegen", []string{"llvm", "abi", "pgo", "mir", "types", "borrow", "source"}},
	{"pgo", []string{"source"}},
	{"llvm", nil},
	{"abi", []string{"types", "source"}},
	{"mono", []string{"mir", "types", "iface", "diag", "fmtspec"}},
	{"gen", []string{"eval", "syntax", "ast", "types", "mir", "diag", "source"}},
	{"eval", []string{"mir", "types", "diag", "source"}},
	{"borrow", []string{"mir", "types", "diag", "source"}},
	{"lower", []string{"check", "ast", "mir", "types", "diag", "source", "fmtspec"}},
	{"check", []string{"resolve", "ast", "types", "mir", "diag", "source", "fmtspec"}},
	{"fmtspec", nil},
	{"resolve", []string{"ast", "types", "diag", "source"}},
	{"syntax", []string{"ast", "diag", "source"}},
	{"mir", []string{"types", "source"}},
	{"types", []string{"source"}},
	{"ast", []string{"source"}},
	{"diag", []string{"source"}},
	{"source", nil},
	{"std", nil},
	{"archtest", nil},
	{"tools/gate", []string{"tools/machinelock"}},
	{"tools/bench", []string{"tools/machinelock"}},
	{"tools/bench/peakspawn", nil},
	{"tools/dogfood", []string{"tools/machinelock"}},
	{"tools/machinelock", nil},
	{"tools/synth", nil},
	// The release tool: the archive format and keys, the tree hash, the tag
	// read.
	{"tools/release", []string{"release", "fetch", "git"}},
}

// TestEdges are imports that only a package's _test.go files may make,
// beyond what the package reaches in DAG. They build test inputs from real
// source with the parser; none is an edge of the compiler, so none changes
// the hierarchy, and none can form a Go import cycle because no target of a
// test edge reaches the package that imports it (TestTestEdgesAreSafe).
// lower's tests may also import eval, to run lowered bodies; mono's tests
// may build instances from real source through syntax, check and lower, and
// codegen's native tests and llvm's syntax smoke test may use toolchain's
// discovery, so they run the clang mk runs; gen's tests may
// lower Mk test generators through resolve, check and lower. corpus's tests
// may import eval for the soundness runner with no entry here: corpus
// reaches eval through driver, and TestTestEdgesAreSafe refuses a test edge
// that is already reachable.
var TestEdges = []Edge{
	{"resolve", []string{"syntax"}},
	{"check", []string{"syntax"}},
	{"lower", []string{"syntax", "eval"}},
	{"mono", []string{"syntax", "resolve", "ast", "check", "lower"}},
	{"codegen", []string{"toolchain"}},
	{"llvm", []string{"toolchain"}},
	{"gen", []string{"resolve", "check", "lower"}},
}

// MaxFileLines is the longest a .go file may be.
const MaxFileLines = 1200
