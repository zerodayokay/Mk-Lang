// Why: the codegen table (tests/bench/README.md "The codegen table") links
// a C object compiled by clang 21 into a Mk program, and mk build has no
// flag for that.
//
// What: shimConfig, writeShimDir (the shim directory), runShim (the shim
// itself), shimStep, shimArgs and backendIRArgs.
//
// How: mk build --llvm <dir> takes clang, lld-link, ld.lld and llvm-lib from
// one directory (internal/toolchain Discover's first rule, FlagDir).
// tools/bench fills such a directory with copies of its own executable under
// those names and a bench-shim.json. Started under one of those names, the
// executable is a shim: it runs the real tool of the same name from RealDir
// with the same arguments, standard streams and exit status, and a linker
// shim appends Objects to the command line. The real tools are the ones every
// other build uses, so the code mk emits does not change; the object is
// linked natively (no LTO for it). Every forwarded command is appended to
// Log, so tools/bench can check that the object reached the link.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// shimConfigName is the file beside the shim copies.
const shimConfigName = "bench-shim.json"

// shimTools are the names mk looks for in its --llvm directory.
var shimTools = []string{"clang", "lld-link", "ld.lld", "llvm-lib"}

type shimConfig struct {
	RealDir string   `json:"real_dir"` // where the real LLVM tools are
	Objects []string `json:"objects"`  // appended to every lld-link and ld.lld command
	Log     string   `json:"log"`      // one line per forwarded command
	// The rest is for `bench ir` (irview.go), which reads a release
	// build's LTO without changing what it emits. A ThinLTO backend is a
	// clang run with -fthinlto-index= (internal/toolchain thinLink); an
	// index step is an lld-link run with /thinlto-index-only; every other
	// lld-link run is a final link.
	BackendArgs []string `json:"backend_args,omitempty"` // appended to each ThinLTO backend (remarks)
	IndexArgs   []string `json:"index_args,omitempty"`   // appended to each index step (import report)
	FinalArgs   []string `json:"final_args,omitempty"`   // appended to each final lld-link (a map file)
	// BackendIRDir, when set, gets each backend module's post-LTO IR: after
	// the backend succeeds, the same command again with -S -emit-llvm into
	// <BackendIRDir>/<module base name>.ll. The object is the first run's.
	BackendIRDir string `json:"backend_ir_dir,omitempty"`
	// StderrDir, when set, keeps each forwarded command's standard error
	// in a file of its own (mk discards a tool's stderr when it succeeds):
	// backend-<module>.txt, index-<pid>.txt, or <tool>-<pid>.txt.
	StderrDir string `json:"stderr_dir,omitempty"`
}

// shimStep is which step of a link args is: "backend" with the module's
// base name, "index", or "" for any other run.
func shimStep(tool string, args []string) (string, string) {
	switch tool {
	case "clang":
		// mk's -### probe (internal/toolchain cc1.go) prints the backend's
		// -cc1 line and runs nothing: not a step.
		if slices.Contains(args, "-###") {
			return "", ""
		}
		mod := ""
		backend := false
		for i, a := range args {
			if strings.HasPrefix(a, "-fthinlto-index=") {
				backend = true
			}
			if a == "ir" && i > 0 && args[i-1] == "-x" && i+1 < len(args) {
				mod = filepath.Base(args[i+1])
			}
		}
		if backend {
			return "backend", mod
		}
	case "lld-link":
		if slices.Contains(args, "/thinlto-index-only") {
			return "index", ""
		}
	}
	return "", ""
}

// backendIRArgs is a backend's command line with its object output
// replaced by textual IR at out. A -cc1 line (mk runs the probed -cc1
// form, internal/toolchain cc1.go) swaps -emit-obj for -emit-llvm; a
// driver line drops -c and adds -S -emit-llvm.
func backendIRArgs(full []string, out string) []string {
	cc1 := slices.Contains(full, "-cc1")
	var r []string
	for i := 0; i < len(full); i++ {
		switch full[i] {
		case "-c":
			if !cc1 {
				continue
			}
		case "-emit-obj":
			if cc1 {
				r = append(r, "-emit-llvm")
				continue
			}
		case "-o":
			i++
			continue
		}
		r = append(r, full[i])
	}
	if cc1 {
		return append(r, "-o", out)
	}
	return append(r, "-S", "-emit-llvm", "-o", out)
}

// shimTool is the tool name argv0 stands for, or "" when the executable runs
// as tools/bench.
func shimTool(argv0 string) string {
	base := strings.ToLower(filepath.Base(argv0))
	base = strings.TrimSuffix(base, ".exe")
	for _, t := range shimTools {
		if base == t {
			return t
		}
	}
	return ""
}

// shimArgs is the real command line of a shim run: the tool in RealDir, the
// arguments, and the objects for a linker.
func shimArgs(cfg shimConfig, tool string, args []string, exe string) []string {
	out := []string{filepath.Join(cfg.RealDir, tool+exe)}
	out = append(out, args...)
	if tool == "lld-link" || tool == "ld.lld" {
		out = append(out, cfg.Objects...)
	}
	switch step, _ := shimStep(tool, args); {
	case step == "backend":
		out = append(out, cfg.BackendArgs...)
	case step == "index":
		out = append(out, cfg.IndexArgs...)
	case tool == "lld-link":
		out = append(out, cfg.FinalArgs...)
	}
	return out
}

// runShim forwards one tool run. Errors of the shim itself exit 125, so they
// are never taken for the tool's own status.
func runShim(tool, argv0 string, args []string) int {
	self, err := os.Executable()
	if err != nil {
		self = argv0
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(self), shimConfigName))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench shim %s: %v\n", tool, err)
		return 125
	}
	var cfg shimConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "bench shim %s: %s: %v\n", tool, shimConfigName, err)
		return 125
	}
	exe := ""
	if strings.HasSuffix(strings.ToLower(self), ".exe") {
		exe = ".exe"
	}
	full := shimArgs(cfg, tool, args, exe)
	if cfg.Log != "" {
		if f, err := os.OpenFile(cfg.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, strings.Join(full, " "))
			f.Close()
		}
	}
	step, mod := shimStep(tool, args)
	var errOut io.Writer = os.Stderr
	if cfg.StderrDir != "" {
		name := fmt.Sprintf("%s-%d.txt", tool, os.Getpid())
		switch step {
		case "backend":
			name = "backend-" + mod + ".txt"
		case "index":
			name = fmt.Sprintf("index-%d.txt", os.Getpid())
		}
		if f, err := os.Create(filepath.Join(cfg.StderrDir, name)); err == nil {
			defer f.Close()
			fmt.Fprintln(f, "#", strings.Join(full, " "))
			errOut = io.MultiWriter(os.Stderr, f)
		}
	}
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, errOut
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "bench shim %s: %v\n", tool, err)
		return 125
	}
	if step == "backend" && cfg.BackendIRDir != "" && mod != "" {
		ir := exec.Command(full[0], backendIRArgs(full[1:], filepath.Join(cfg.BackendIRDir, mod+".ll"))...)
		if b, err := ir.CombinedOutput(); err != nil {
			// The build itself succeeded; the missing IR file says so.
			fmt.Fprintf(os.Stderr, "bench shim: post-LTO IR of %s: %v\n%s", mod, err, firstLines(string(b), 10))
		}
	}
	return 0
}

// writeShimDir makes dir a --llvm directory: a copy of this executable per
// tool name, and the config.
func writeShimDir(dir string, cfg shimConfig) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	exe := ""
	if strings.HasSuffix(strings.ToLower(self), ".exe") {
		exe = ".exe"
	}
	for _, t := range shimTools {
		if err := os.WriteFile(filepath.Join(dir, t+exe), body, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, shimConfigName), append(b, '\n'), 0o644)
}
