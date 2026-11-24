// Why: the borrow checker has a size budget, and its numbers and the standard
// library's size should be visible on every test run.
// What: TestBudgetSourceSizes, which logs Go and Mk source sizes, and
// TestCodeLinesOf.
// How: counts token-bearing lines with go/scanner and finds diagnostic code
// through calls to diag.New with go/ast; it never fails on size.

package corpus

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Budgets that TestBudgetSourceSizes logs: the borrow and linear checker at
// most 8,000 lines of Go with at most 40% diagnostics. The test never fails
// on them; it only records the numbers.
const (
	budgetBorrowLines = 8000
	budgetBorrowDiag  = 0.40
)

// goSize is the measure of one package's non-test Go files: code lines are the
// lines holding at least one token that is not a comment (go/scanner), and the
// diagnostic lines are the code lines of the top-level functions whose body
// calls diag.New (go/ast). Transitive also counts the functions that reach
// diag.New through a same-package call by name, for the reader: the spec's
// measure is the direct one.
type goSize struct {
	Files, Code, Diag, DiagTransitive int
	DiagFuncs                         []string
}

// TestBudgetSourceSizes logs the borrow and std size rows. It fails only if
// it cannot read the files it measures.
func TestBudgetSourceSizes(t *testing.T) {
	root := repoRoot(t)
	b, err := measureGo(filepath.Join(root, "internal", "borrow"))
	if err != nil {
		t.Fatal(err)
	}
	share := func(n int) float64 {
		if b.Code == 0 {
			return 0
		}
		return float64(n) / float64(b.Code)
	}
	t.Logf("internal/borrow: %d non-test files, %d code lines (budget %d: %s)", b.Files, b.Code, budgetBorrowLines, within(b.Code <= budgetBorrowLines))
	t.Logf("internal/borrow: diagnostics %d lines, %.1f%% (functions calling diag.New: %s; budget %.0f%%: %s)",
		b.Diag, 100*share(b.Diag), strings.Join(b.DiagFuncs, ", "), 100*budgetBorrowDiag, within(share(b.Diag) <= budgetBorrowDiag))
	t.Logf("internal/borrow: with the functions that reach diag.New through same-package calls, %d lines, %.1f%% (not the spec's measure)",
		b.DiagTransitive, 100*share(b.DiagTransitive))
	files, lines, code, err := measureMk(filepath.Join(root, "std"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("std: %d .mk files, %d lines, %d of them not blank", files, lines, code)
}

func within(ok bool) string {
	if ok {
		return "within"
	}
	return "OVER"
}

// measureGo measures the non-test .go files directly in dir.
func measureGo(dir string) (goSize, error) {
	var s goSize
	ents, err := os.ReadDir(dir)
	if err != nil {
		return s, err
	}
	fset := token.NewFileSet()
	type fn struct {
		name   string
		lines  int
		direct bool
		calls  []string
	}
	var fns []fn
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return s, err
		}
		s.Files++
		codeLines := codeLinesOf(fset, name, src)
		s.Code += len(codeLines)
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return s, err
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			from, to := fset.Position(fd.Pos()).Line, fset.Position(fd.End()).Line
			n := 0
			for l := from; l <= to; l++ {
				if codeLines[l] {
					n++
				}
			}
			g := fn{name: fd.Name.Name, lines: n}
			ast.Inspect(fd.Body, func(x ast.Node) bool {
				c, ok := x.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := c.Fun.(type) {
				case *ast.SelectorExpr:
					if id, ok := f.X.(*ast.Ident); ok && id.Name == "diag" && f.Sel.Name == "New" {
						g.direct = true
					} else {
						g.calls = append(g.calls, f.Sel.Name)
					}
				case *ast.Ident:
					g.calls = append(g.calls, f.Name)
				}
				return true
			})
			fns = append(fns, g)
		}
	}
	reach := map[string]bool{}
	for _, f := range fns {
		if f.direct {
			reach[f.name] = true
			s.Diag += f.lines
			s.DiagFuncs = append(s.DiagFuncs, f.name)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, f := range fns {
			if reach[f.name] {
				continue
			}
			for _, c := range f.calls {
				if reach[c] {
					reach[f.name], changed = true, true
					break
				}
			}
		}
	}
	for _, f := range fns {
		if reach[f.name] {
			s.DiagTransitive += f.lines
		}
	}
	slices.Sort(s.DiagFuncs)
	return s, nil
}

// codeLinesOf returns the set of lines of src that hold a token other than a
// comment. A token spanning lines (a raw string) marks every line it covers.
func codeLinesOf(fset *token.FileSet, name string, src []byte) map[int]bool {
	lines := map[int]bool{}
	file := fset.AddFile(name, -1, len(src))
	var sc scanner.Scanner
	sc.Init(file, src, nil, 0) // comments are skipped
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue // an inserted semicolon is not a token of the line
		}
		from := file.Line(pos)
		to := from + strings.Count(lit, "\n")
		for l := from; l <= to; l++ {
			lines[l] = true
		}
	}
	return lines
}

// measureMk counts the .mk files under dir, their lines, and the lines that
// are not blank.
func measureMk(dir string) (files, lines, code int, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		for _, l := range strings.Split(strings.TrimSuffix(string(src), "\n"), "\n") {
			lines++
			if strings.TrimSpace(l) != "" {
				code++
			}
		}
		return nil
	})
	return files, lines, code, err
}

// TestCodeLinesOf pins the counting rule on a small file.
func TestCodeLinesOf(t *testing.T) {
	src := "package p\n\n// comment\nfunc F() { /* c */ }\n/*\nblock\n*/\nvar s = `a\nb`\n"
	got := codeLinesOf(token.NewFileSet(), "p.go", []byte(src))
	var ls []int
	for l := range got {
		ls = append(ls, l)
	}
	slices.Sort(ls)
	if want := []int{1, 4, 8, 9}; !slices.Equal(ls, want) {
		t.Fatalf("code lines %v, want %v", ls, want)
	}
}
