// Why: a same-algorithm row compares compilers only while every column uses
// the same data structures, loop structure and arithmetic types.
// What: TestBinarytreesOnePoolShape and TestSameArithmeticTypes.
// How: reads each column's source text and checks the binarytrees pool shape
// and the declared type of each accumulator variable across languages.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// treeRows is the same-algorithm rows of the repository.
func treeRows(t *testing.T) []*row {
	t.Helper()
	wd, _ := os.Getwd()
	root, err := repoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	var out []*row
	for _, r := range rows {
		if r.Lane == "" && r.Table == "same" {
			out = append(out, r)
		}
	}
	return out
}

// sourceText is the text of language lang's source of r (an Mk source is a
// package directory: its <row>.mk).
func sourceText(t *testing.T, r *row, lang string) string {
	t.Helper()
	p := filepath.Join(r.Dir, r.Sources[lang])
	if lang == "mk" || lang == "mk-wrap" {
		p = filepath.Join(p, r.Name+".mk")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestBinarytreesOnePoolShape: every binarytrees column keeps the node
// buffer and the bump index together in one pool passed to build by
// pointer, and none keeps them in file-scope state (C's statics let clang
// prove that node stores never alias top, which the others cannot).
func TestBinarytreesOnePoolShape(t *testing.T) {
	shape := map[string][]string{
		"c":    {`struct pool \{`, `struct node \*nodes;`, `uint32_t top;`, `uint32_t build\(struct pool \*p, int64_t depth\)`, `int64_t check\(const struct node \*nodes, uint32_t idx\)`},
		"rust": {`struct Pool \{`, `nodes: Vec<Node>,`, `top: u32,`, `fn build\(p: &mut Pool, depth: i64\) -> u32`, `fn check\(nodes: &\[Node\], idx: u32\) -> i64`},
		"zig":  {`const Pool = struct \{`, `nodes: \[\]Node,`, `top: u32,`, `fn build\(p: \*Pool, depth: i64\) u32`, `fn check\(nodes: \[\]const Node, idx: u32\) i64`},
		"go":   {`type pool struct \{`, `nodes \[\]node`, `top\s+uint32`, `func build\(p \*pool, depth int\) uint32`, `func check\(nodes \[\]node, idx uint32\) int`},
		"cs":   {`struct Pool`, `public Node\[\] Nodes;`, `public uint Top;`, `uint Build\(ref Pool p, long depth\)`, `long Check\(Node\[\] nodes, uint idx\)`},
		"mk":   {`struct Pool \{`, `nodes: mut \[\]Node`, `top: u32`, `Build\(p: mut Pool, depth: int\): u32`, `Check\(nodes: \[\]Node, idx: u32\): int`},
	}
	global := map[string]*regexp.Regexp{
		"c":    regexp.MustCompile(`(?m)^static\s+[^(]*;`),
		"rust": regexp.MustCompile(`(?m)^static\s`),
		"zig":  regexp.MustCompile(`(?m)^var\s`),
		"go":   regexp.MustCompile(`(?m)^var\s`),
		"cs":   regexp.MustCompile(`(?m)^\s*(?:private\s+|internal\s+|public\s+)?static\s+(?:Node|Pool)\b`),
		"mk":   regexp.MustCompile(`(?m)^var\s`),
	}
	var bt *row
	for _, r := range treeRows(t) {
		if r.Name == "binarytrees" {
			bt = r
		}
	}
	if bt == nil {
		t.Fatal("no binarytrees row")
	}
	for lang, res := range shape {
		src := stripComments(sourceText(t, bt, lang))
		for _, re := range res {
			if !regexp.MustCompile(re).MatchString(src) {
				t.Errorf("binarytrees %s: no %s; every column passes one Pool{nodes, top} to build by pointer (BC-6)", lang, re)
			}
		}
		if m := global[lang].FindString(src); m != "" {
			t.Errorf("binarytrees %s: file-scope state %q; the pool is passed, never global (BC-6)", lang, m)
		}
	}
}

// accumulators are the variables whose type the benchmark rules require to be
// the same in every column of a row (indices are not among them: Rust and
// Zig index with usize, as the rows' READMEs state). Per row, extra names.
var accumulators = []string{"total", "sum", "count", "flips", "checksum"}

var rowAccumulators = map[string][]string{"jsonscan": {"l"}}

// declType finds the declared type of variable name in a source of lang,
// canonicalized ("i64", "u64", "f64", "usize", ...), or "" when the source
// declares it without a type the test can read.
func declType(lang, src, name string) string {
	q := regexp.QuoteMeta(name)
	var pats []string
	switch lang {
	case "c":
		pats = []string{`\b(int64_t|uint64_t|int32_t|uint32_t|size_t|double|int)\s+` + q + `\s*=`}
	case "rust":
		pats = []string{`\blet\s+(?:mut\s+)?` + q + `\s*:\s*(\w+)\s*=`}
	case "zig":
		pats = []string{`\b(?:var|const)\s+` + q + `\s*:\s*(\w+)\s*=`}
	case "go":
		pats = []string{`(?m)^\s*` + q + `\s*:=\s*(0\.0|0)\s*$`, `\bvar\s+` + q + `\s+(\w+)`}
	case "mk", "mk-wrap":
		pats = []string{`\bvar\s+` + q + `\s*:\s*(\w+)\s*=`, `(?m)\bvar\s+` + q + `\s*:=\s*(0\.0|0)\s*$`}
	case "cs":
		pats = []string{`\b(long|ulong|int|uint|double)\s+` + q + `\s*=`}
	}
	for _, p := range pats {
		if m := regexp.MustCompile(p).FindStringSubmatch(src); m != nil {
			return canonType(lang, m[1])
		}
	}
	return ""
}

func canonType(lang, t string) string {
	switch t {
	case "int64_t", "i64", "int64", "long":
		return "i64"
	case "uint64_t", "u64", "uint64", "ulong":
		return "u64"
	case "int32_t", "i32", "int32":
		return "i32"
	case "uint32_t", "u32", "uint32", "uint":
		return "u32"
	case "double", "f64", "float64", "0.0":
		return "f64"
	case "size_t", "usize":
		return "usize"
	case "0":
		return "i64" // Go's and Mk's int, 64-bit on x86-64
	case "int":
		if lang == "c" || lang == "cs" {
			return "i32"
		}
		return "i64"
	}
	return "" // a type this test does not read (a Vec, a slice)
}

// TestSameArithmeticTypes: every column of a same row declares each
// accumulator with the same type, so no column gets another optimizer
// licence (unsigned usize gives Zig nuw where C's int64_t gives nsw).
func TestSameArithmeticTypes(t *testing.T) {
	checked := 0
	for _, r := range treeRows(t) {
		names := append(append([]string(nil), accumulators...), rowAccumulators[r.Name]...)
		for _, name := range names {
			types := map[string]string{}
			for lang := range r.Sources {
				if ty := declType(lang, stripComments(sourceText(t, r, lang)), name); ty != "" {
					types[lang] = ty
				}
			}
			if len(types) < 2 {
				continue
			}
			checked++
			var first string
			for _, ty := range types {
				first = ty
				break
			}
			for _, ty := range types {
				if ty != first {
					t.Errorf("%s: %s is declared with different types across columns %v; the README's types hold in every column (BC-7)", r.id(), name, types)
					break
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d (row, variable) pairs were compared; the declaration patterns no longer match the sources", checked)
	}
	// The case of the finding: usize sums against C's int64_t.
	if a, b := declType("rust", "let mut total: usize = 0;", "total"), declType("c", "int64_t total = 0;", "total"); a == b {
		t.Fatalf("declType does not tell usize from int64_t: %q %q", a, b)
	}
	if got := declType("mk", "            var l := 0\n", "l"); got != "i64" {
		t.Fatalf("declType(mk, var l := 0) = %q", got)
	}
	if got := declType("go", "\t\t\tl := 0\n", "l"); got != "i64" {
		t.Fatalf("declType(go, l := 0) = %q", got)
	}
}
