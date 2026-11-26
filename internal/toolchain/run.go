// Why: every tool mk runs must see the same environment on every machine,
// and its failures must be told apart: a crash, a rejected input, a missing
// tool and a cancelled build are different diagnostics.
//
// What: ToolError, run and runBoth, toolError, scrubbedEnv and
// CommandLine.
//
// How: a tool gets a scrubbed, fixed-order environment (SOURCE_DATE_EPOCH=0,
// LC_ALL=C, ZERO_AR_DATE=1 and a few host variables); toolError checks the
// context before the exit status, because a process killed for a done
// context looks like a real failure on Windows.

package toolchain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

// The ExitCode values that are not a tool's exit status.
const (
	// ExitNotStarted: no process ran (a missing tool, a failed start, or a
	// context that was done before the start).
	ExitNotStarted = -1
	// ExitNoStatus: the process started and ended without an exit status,
	// killed by a signal (Signal) or by the context (Cancelled).
	ExitNoStatus = -2
)

// ToolError is a tool that could not start, exited non-zero, died by a
// signal, or was stopped because its context ended. The driver checks the
// cases in this order: Cancelled is no diagnostic of the
// tool (the build is being stopped); Signal is a crash, E0000 when the
// input was compiler-produced; ExitNotStarted is E0001; an exit status is
// E0001, or E0000 when the tool rejected IR the compiler wrote. The facts
// are tool, command and stderr.
type ToolError struct {
	Tool      string // "clang", "lld-link", "ld.lld", "llvm-lib"
	Command   string // the full command line, each argument quoted when needed
	Stderr    string // verbatim; the driver cuts it to diag.StderrLimit
	ExitCode  int    // the exit status, or ExitNotStarted, or ExitNoStatus
	Signal    string // the signal that ended the process ("segmentation fault"), Unix only
	Cancelled bool   // the context was done when the tool ended; Err wraps ctx.Err()
	Err       error
}

// Started reports whether a process ran at all.
func (e *ToolError) Started() bool { return e.ExitCode != ExitNotStarted }

func (e *ToolError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	switch {
	case e.Cancelled:
		cause := context.Canceled
		if errors.Is(e.Err, context.DeadlineExceeded) {
			cause = context.DeadlineExceeded
		}
		return fmt.Sprintf("%s was cancelled: %v (%s)", e.Tool, cause, e.Command)
	case e.Signal != "":
		return fmt.Sprintf("%s was killed by signal %s: %s (%s)", e.Tool, e.Signal, msg, e.Command)
	case e.ExitCode < 0:
		return fmt.Sprintf("%s could not start: %v (%s)", e.Tool, e.Err, e.Command)
	}
	return fmt.Sprintf("%s exited with status %d: %s", e.Tool, e.ExitCode, msg)
}

func (e *ToolError) Unwrap() error { return e.Err }

// errNoTool is the cause of a ToolError for a tool Discover did not find.
var errNoTool = errors.New("not found in the chosen LLVM directory")

// run is the one process runner every tool goes through: an absolute path,
// no shell, the scrubbed environment of scrubbedEnv, working directory dir
// ("" means the current one), stdout returned, stderr captured into the
// ToolError. A non-zero exit, a signal, a cancellation or a failure to
// start is *ToolError.
func run(ctx context.Context, tool, path string, args []string, dir string) ([]byte, error) {
	out, _, err := runBoth(ctx, tool, path, args, dir)
	return out, err
}

// runBoth is run that also returns stderr on success, for the queries whose
// answer clang prints there (-###, ResolveCPU).
func runBoth(ctx context.Context, tool, path string, args []string, dir string) ([]byte, []byte, error) {
	cmdline := CommandLine(path, args)
	if path == "" {
		return nil, nil, &ToolError{Tool: tool, Command: cmdline, ExitCode: ExitNotStarted, Err: errNoTool}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = scrubbedEnv(runtime.GOOS, os.Getenv)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), nil
	}
	return stdout.Bytes(), stderr.Bytes(), toolError(ctx, tool, cmdline, stderr.String(), cmd.ProcessState, err)
}

// toolError classifies a failed run. The context comes first: on Windows a
// process killed for a done context reports exit status 1 (TerminateProcess),
// the shape of a real tool failure. Then a process that did not
// exit by itself is a signal, never "did not start".
func toolError(ctx context.Context, tool, cmdline, stderr string, ps *os.ProcessState, err error) *ToolError {
	te := &ToolError{Tool: tool, Command: cmdline, Stderr: stderr, ExitCode: ExitNotStarted, Err: err}
	if ps != nil {
		te.ExitCode = ExitNoStatus
	}
	if cerr := ctx.Err(); cerr != nil {
		te.Cancelled = true
		if !errors.Is(err, cerr) {
			te.Err = errors.Join(cerr, err)
		}
		return te
	}
	if ps == nil {
		return te
	}
	if ps.Exited() {
		te.ExitCode = ps.ExitCode()
		return te
	}
	te.Signal = strings.TrimPrefix(ps.String(), "signal: ")
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		te.Signal = ws.Signal().String()
	}
	return te
}

// scrubbedEnv is the whole environment a tool sees. Windows keeps only
// SystemRoot, SystemDrive, TEMP and TMP from the host; Linux gets
// PATH=/usr/bin:/bin and keeps TMPDIR; both get SOURCE_DATE_EPOCH=0,
// LC_ALL=C, ZERO_AR_DATE=1 and CLANG_NO_DEFAULT_CONFIG=1. The order is
// fixed. Every other host fact is either absent or part of Tools.Identity.
// The last keeps clang from reading its default configuration files
// (<triple>.cfg and clang.cfg beside it or in a configuration directory),
// whose flags no cache key holds; it is the environment and not
// --no-default-config because a -cc1 command line refuses that flag.
func scrubbedEnv(goos string, getenv func(string) string) []string {
	var env []string
	keep := func(k string) {
		if v := getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if goos == "windows" {
		keep("SystemRoot")
		keep("SystemDrive")
		keep("TEMP")
		keep("TMP")
	} else {
		env = append(env, "PATH=/usr/bin:/bin")
		keep("TMPDIR")
	}
	return append(env, "SOURCE_DATE_EPOCH=0", "LC_ALL=C", "ZERO_AR_DATE=1", "CLANG_NO_DEFAULT_CONFIG=1")
}

// CommandLine renders a command for E0001's `command` fact and for logs: the
// path and each argument separated by one space, an argument quoted with
// double quotes when it is empty or holds a space, tab, newline or double
// quote, and then `"` and `\` inside it are escaped by `\`.
// It is for reading, never run through a shell.
func CommandLine(path string, args []string) string {
	var b strings.Builder
	b.WriteString(quoteArg(path))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(quoteArg(a))
	}
	return b.String()
}

func quoteArg(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\n\"") {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(a); i++ {
		if a[i] == '"' || a[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(a[i])
	}
	b.WriteByte('"')
	return b.String()
}
