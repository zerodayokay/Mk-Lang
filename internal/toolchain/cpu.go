// Why: --cpu native must not enter a cache key as "native": two hosts would
// share objects built for different CPUs.
//
// What: CPUNative and ResolveCPU, which turns "native" into clang's
// explicit CPU name.
//
// How: a dry run (-###) of a compile with -march=native (-mcpu=native on
// AArch64), reading the value after -target-cpu.

package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// CPUNative is the --cpu value that names the host's CPU.
const CPUNative = "native"

// ResolveCPU returns the explicit CPU name a build for triple compiles for
// when the user asked for cpu.
//
// "native" is resolved to the host's CPU as clang names it: the
// -target-cpu clang's driver passes to cc1 for -march=native (-mcpu=native
// on AArch64), read from `clang -###` over an empty IR file, which starts no
// compile. The caller refuses "native" for a triple of another
// architecture than the host's first: clang answers with the host's CPU
// for any triple.
//
// Any other name is checked by compiling an empty IR module with exactly
// the flag Compile passes (compileArgs), so a name clang does not know for
// the triple fails here, once, before any pack is compiled. Such a
// rejection is *ToolError with Started true and clang's message ("unknown
// target CPU 'x'") in Stderr; a clang that cannot run is *ToolError with
// Started false. A ToolError's Command names the temporary directory as
// <work>, so its random name reaches no output. "" is returned as "" (the
// caller's baseline).
func (t *Tools) ResolveCPU(ctx context.Context, triple, cpu string) (string, error) {
	if cpu == "" {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "mk-cpu-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	empty := filepath.Join(dir, "empty.ll")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "empty.o")
	if cpu == CPUNative {
		_, stderr, err := runBoth(ctx, NameClang, t.Clang, nativeArgs(triple, empty, out), dir)
		if err != nil {
			return "", scrubDir(err, dir)
		}
		name, ok := targetCPU(string(stderr))
		if !ok || name == CPUNative {
			return "", errors.New("clang -### printed no -target-cpu for -march=native:\n" + firstLinesOf(string(stderr), 20))
		}
		return name, nil
	}
	args, err := compileArgs(CompileJob{Triple: triple, Input: empty, Output: out, Opt: O0, CPU: cpu})
	if err != nil {
		return "", err
	}
	if _, err := run(ctx, NameClang, t.Clang, args, dir); err != nil {
		return "", scrubDir(err, dir)
	}
	return cpu, nil
}

// scrubDir replaces dir, in its OS and slash forms, by <work> in a
// ToolError's command line.
func scrubDir(err error, dir string) error {
	var te *ToolError
	if errors.As(err, &te) {
		for _, d := range []string{dir, filepath.ToSlash(dir)} {
			te.Command = strings.ReplaceAll(te.Command, d, "<work>")
		}
	}
	return err
}

// nativeArgs is ResolveCPU's clang command line for "native": the
// driver's dry run (-###) of a compile with -march=native, or -mcpu=native
// for an AArch64 triple.
func nativeArgs(triple, empty, out string) []string {
	flag := "-march=" + CPUNative
	if ArchOf(triple) == "arm64" {
		flag = "-mcpu=" + CPUNative
	}
	return []string{ClangTargetArg(triple), flag, "-###", "-x", "ir", "-c", empty, "-o", out}
}

// targetCPU reads the value after "-target-cpu" in clang's -### output,
// whose arguments are each double-quoted.
func targetCPU(out string) (string, bool) {
	const key = `"-target-cpu" "`
	i := strings.Index(out, key)
	if i < 0 {
		return "", false
	}
	rest := out[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j <= 0 {
		return "", false
	}
	return rest[:j], true
}

// firstLinesOf is at most n lines of s.
func firstLinesOf(s string, n int) string {
	ls := strings.SplitN(s, "\n", n+1)
	if len(ls) > n {
		ls = ls[:n]
	}
	return strings.Join(ls, "\n")
}
