// Why: codegen must print the exact datalayout and triple clang uses for a
// target, or every compile warns about an overridden module.
//
// What: TargetFacts, its Header, and Probe.
//
// How: clang compiles an empty IR file to text; the two lines are read
// from the output, and the driver caches them by (Identity, triple).

package toolchain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TargetFacts is what clang says about a triple.
type TargetFacts struct {
	Triple     string // clang's normalized triple, e.g. x86_64-pc-windows-msvc19.33.0
	DataLayout string // the `target datalayout` string
}

// Header is the two module lines codegen emits verbatim at the top of every
// `.ll`, so Compile sees no -Woverride-module.
func (f TargetFacts) Header() string {
	return fmt.Sprintf("target datalayout = %q\ntarget triple = %q\n", f.DataLayout, f.Triple)
}

// Probe runs `clang --target=<triple> -x ir -S -emit-llvm -o - <empty.ll>`
// on an empty IR file (never C) in a temporary directory and reads
// the `target datalayout` and `target triple` lines. clang's warnings are
// ignored. The driver caches the result by (Identity, triple). The error is
// *ToolError, or says a line was missing from clang's output.
func (t *Tools) Probe(ctx context.Context, triple string) (TargetFacts, error) {
	dir, err := os.MkdirTemp("", "mk-probe-")
	if err != nil {
		return TargetFacts{}, err
	}
	defer os.RemoveAll(dir)
	empty := filepath.Join(dir, "empty.ll")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		return TargetFacts{}, err
	}
	out, err := run(ctx, NameClang, t.Clang, probeArgs(triple, empty), dir)
	if err != nil {
		return TargetFacts{}, err
	}
	return parseProbe(string(out))
}

// probeArgs is the probe's clang command line after the executable.
func probeArgs(triple, empty string) []string {
	return []string{ClangTargetArg(triple), "-x", "ir", "-S", "-emit-llvm", "-o", "-", empty}
}

// parseProbe reads the two lines from clang's textual module.
func parseProbe(out string) (TargetFacts, error) {
	var f TargetFacts
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := quotedAfter(line, "target datalayout"); ok {
			f.DataLayout = v
		} else if v, ok := quotedAfter(line, "target triple"); ok {
			f.Triple = v
		}
	}
	if f.DataLayout == "" || f.Triple == "" {
		return f, fmt.Errorf("clang printed no target datalayout or target triple line:\n%s", out)
	}
	return f, nil
}

// quotedAfter reads `<key> = "<value>"`.
func quotedAfter(line, key string) (string, bool) {
	rest, ok := strings.CutPrefix(line, key)
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	rest, ok = strings.CutPrefix(rest, "=")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
		return "", false
	}
	return rest[1 : len(rest)-1], true
}
