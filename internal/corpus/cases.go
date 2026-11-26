// Why: test programs under tests/programs are run with arguments, inputs and
// expected outputs written in <name>.cases files; a typo in such a file must
// be an error, never a case that silently checks less.
//
// What: ProgramCase, LoadCases (every cases file under the programs suite)
// and ParseCases (one file, without the disk).
//
// How: a strict line parser: an unknown key, profile, target or missing file
// is an error naming path:line. Paths are repository-relative with "/"
// separators and may not leave tests/programs.

package corpus

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ProgramsDir is the programs suite, repository-relative.
const ProgramsDir = "tests/programs"

// DefaultCaseTimeout is a case's timeout when it states none. Thirty seconds
// leaves headroom for synchronous first-run executable scans on Windows even
// when builds run alongside it.
const DefaultCaseTimeout = 30 * time.Second

// The profiles and targets a case may name, in their default order.
var (
	caseProfiles = []string{"dev", "release"}
	caseTargets  = []string{"x86_64-windows", "x86_64-linux"}
)

// ProgramCase is one run of a test program. Every path is
// repository-relative with "/" separators.
type ProgramCase struct {
	Program  string   // "tests/programs/wc.mk", or a directory
	Name     string   // "ok", "missing-file"
	Profiles []string // "dev", "release"; default both
	Targets  []string // "x86_64-windows", "x86_64-linux"; default both
	Args     []string
	Files    []string // copied into the run directory before the run
	Stdin    string   // path of the stdin file, or "" for empty input
	Stdout   string   // path of the expected stdout bytes, or "" for empty
	Stderr   string   // path of the expected stderr bytes, or "" for empty
	Exit     int
	Timeout  time.Duration // default 30 s
	At       string        // "tests/programs/wc.cases:3", the case line, for messages
}

// LoadCases parses every tests/programs/**/<name>.cases file under root, in
// byte order of paths, and returns the cases in file order. Each file's
// program is <name>.mk or the directory <name>/ beside it; each file a case
// names (files, stdin, stdout, stderr) is relative to the .cases file's
// directory and must exist. Any error names path:line.
func LoadCases(root string) ([]ProgramCase, error) {
	var paths []string
	base := filepath.Join(root, filepath.FromSlash(ProgramsDir))
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".cases") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	var out []ProgramCase
	for _, p := range paths {
		text, err := readText(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return nil, err
		}
		cs, err := ParseCases(p, text)
		if err != nil {
			return nil, err
		}
		if err := checkCaseFiles(root, p, cs); err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

// ParseCases parses one cases file, at its repository-relative path p (which
// names the program and anchors the case files), without touching the disk.
func ParseCases(p, text string) ([]ProgramCase, error) {
	dir := path.Dir(p)
	program := strings.TrimSuffix(p, ".cases") + ".mk"
	var out []ProgramCase
	var cur *ProgramCase
	seen := map[string]map[string]bool{}
	errAt := func(line int, format string, args ...any) error {
		return fmt.Errorf("%s:%d: %s", p, line, fmt.Sprintf(format, args...))
	}
	for i, l := range splitLines(text) {
		line := i + 1
		if t := strings.TrimSpace(l); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		key, val, _ := strings.Cut(strings.TrimSpace(l), " ")
		val = strings.TrimSpace(val)
		if key == "case" {
			if val == "" || strings.ContainsAny(val, " \t") {
				return nil, errAt(line, "case %q: want one name", val)
			}
			if slices.ContainsFunc(out, func(c ProgramCase) bool { return c.Name == val }) {
				return nil, errAt(line, "case %s twice", val)
			}
			out = append(out, ProgramCase{Program: program, Name: val, Profiles: caseProfiles, Targets: caseTargets,
				Timeout: DefaultCaseTimeout, At: fmt.Sprintf("%s:%d", p, line)})
			cur = &out[len(out)-1]
			seen[val] = map[string]bool{}
			continue
		}
		if cur == nil {
			return nil, errAt(line, "%s before the first case line", key)
		}
		if seen[cur.Name][key] {
			return nil, errAt(line, "%s twice in case %s", key, cur.Name)
		}
		seen[cur.Name][key] = true
		var err error
		switch key {
		case "profile":
			cur.Profiles, err = caseChoice(val, caseProfiles)
		case "target":
			cur.Targets, err = caseChoice(val, caseTargets)
		case "args":
			cur.Args, err = caseArgs(val)
		case "files":
			for _, f := range strings.Fields(val) {
				var rel string
				if rel, err = caseFile(dir, f); err != nil {
					break
				}
				cur.Files = append(cur.Files, rel)
			}
		case "stdin":
			cur.Stdin, err = caseFile(dir, val)
		case "stdout":
			cur.Stdout, err = caseFile(dir, val)
		case "stderr":
			cur.Stderr, err = caseFile(dir, val)
		case "exit":
			cur.Exit, err = strconv.Atoi(val)
			if err == nil && (cur.Exit < 0 || cur.Exit > 255) {
				err = fmt.Errorf("exit %d: want 0 to 255", cur.Exit)
			}
		case "timeout":
			var n int
			n, err = strconv.Atoi(val)
			if err == nil && n <= 0 {
				err = fmt.Errorf("timeout %d: want a positive number of seconds", n)
			}
			cur.Timeout = time.Duration(n) * time.Second
		default:
			err = fmt.Errorf("unknown key %q", key)
		}
		if err != nil {
			return nil, errAt(line, "%v", err)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no case", p)
	}
	return out, nil
}

// caseChoice is a profile or target value: one or more of allowed, space
// separated, no repeats, returned in allowed's order.
func caseChoice(val string, allowed []string) ([]string, error) {
	fs := strings.Fields(val)
	if len(fs) == 0 {
		return nil, fmt.Errorf("empty value, want one or more of %s", strings.Join(allowed, " "))
	}
	var out []string
	for _, a := range allowed {
		if n := countOf(fs, a); n > 1 {
			return nil, fmt.Errorf("%s twice", a)
		} else if n == 1 {
			out = append(out, a)
		}
	}
	if len(out) != len(fs) {
		return nil, fmt.Errorf("%q: want one or more of %s", val, strings.Join(allowed, " "))
	}
	return out, nil
}

func countOf(xs []string, x string) int {
	n := 0
	for _, y := range xs {
		if y == x {
			n++
		}
	}
	return n
}

// caseArgs splits an args value: space separated, "..." quotes one argument
// with spaces; no escapes.
func caseArgs(val string) ([]string, error) {
	var out []string
	for rest := strings.TrimSpace(val); rest != ""; rest = strings.TrimLeft(rest, " \t") {
		if rest[0] == '"' {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("args: unterminated quote in %q", val)
			}
			out = append(out, rest[1:1+end])
			rest = rest[2+end:]
			if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
				return nil, fmt.Errorf("args: text after a closing quote in %q", val)
			}
			continue
		}
		i := strings.IndexAny(rest, " \t")
		if i < 0 {
			i = len(rest)
		}
		if strings.Contains(rest[:i], `"`) {
			return nil, fmt.Errorf("args: a quote inside an argument in %q", val)
		}
		out = append(out, rest[:i])
		rest = rest[i:]
	}
	return out, nil
}

// caseFile is one file name of a case, relative to the cases file's
// directory dir, as a repository-relative path; it may not leave
// tests/programs.
func caseFile(dir, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, " \t\\") || path.IsAbs(name) {
		return "", fmt.Errorf("file %q: want one relative path with / separators", name)
	}
	p := path.Join(dir, name)
	if p != ProgramsDir && !strings.HasPrefix(p, ProgramsDir+"/") {
		return "", fmt.Errorf("file %q leaves %s", name, ProgramsDir)
	}
	return p, nil
}

// checkCaseFiles requires the program (<name>.mk, or the directory <name>/
// whose Program is then the directory) and every file the cases name.
func checkCaseFiles(root, p string, cs []ProgramCase) error {
	exists := func(rel string, dir bool) bool {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil && fi.IsDir() == dir
	}
	prog := strings.TrimSuffix(p, ".cases")
	switch {
	case exists(prog+".mk", false):
	case exists(prog, true):
		for i := range cs {
			cs[i].Program = prog
		}
	default:
		return fmt.Errorf("%s: no program %s.mk or directory %s/", p, path.Base(prog), path.Base(prog))
	}
	for _, c := range cs {
		for _, f := range append(slices.Clone(c.Files), c.Stdin, c.Stdout, c.Stderr) {
			if f != "" && !exists(f, false) {
				return fmt.Errorf("%s: case %s names %s, which does not exist", c.At, c.Name, f)
			}
		}
	}
	return nil
}
