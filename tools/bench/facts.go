// Why: a timing means nothing without what it was measured on: the
// toolchain versions, the flags, the machine and the date are recorded in
// every table.
//
// What: facts and gatherFacts, toolchain.version, and busyNow, mergeBusy
// and busyProcesses, the processes that compete for the machine.
//
// How: besides the versions, the facts hold the power scheme, the Defender
// state, the CPU affinity, GOGC and what runEnv gives every program. The
// machine may be shared, so the competing processes (compilers, test
// binaries, fuzzers) are counted at the start of a run and of every sweep.

package main

import (
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// facts is the header of a results file.
type facts struct {
	Date     string            `json:"date"`
	Commit   string            `json:"commit"`
	Dirty    bool              `json:"dirty"`
	OS       string            `json:"os"`
	CPU      string            `json:"cpu"`
	Power    string            `json:"power_scheme"`
	Defender string            `json:"defender_realtime"`
	Affinity string            `json:"affinity"`
	GOGC     string            `json:"gogc"`
	RunEnv   string            `json:"run_env,omitempty"` // what runEnv sets and clears on every program
	Versions map[string]string `json:"versions"`
	Busy     []string          `json:"busy_processes"` // compilers, test binaries and fuzzers running at the start
	// BusyLater is the same check at the start of every later sweep,
	// merged: a run that started on a quiet machine but shared it
	// later says so.
	BusyLater   []string `json:"busy_later,omitempty"`
	MkBinary    string   `json:"mk_binary"`
	MkSHA256    string   `json:"mk_sha256"`
	MkStd       string   `json:"mk_std,omitempty"`  // MK_STD of the Mk builds; "" is the std embedded in mk
	Started     string   `json:"started,omitempty"` // when the run began (Date is the results directory)
	Provisional bool     `json:"provisional"`
}

// output runs a command and returns its trimmed output, or "" on failure.
func output(name string, args ...string) string {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// outputEnv runs a command with extra env vars and returns its trimmed output.
func outputEnv(env []string, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(l)
}

// gatherFacts fills what can be read without the network.
func gatherFacts(root string, t *toolchain) facts {
	f := facts{OS: runtime.GOOS + "/" + runtime.GOARCH, Versions: map[string]string{}}
	// What the programs get, read back from the environment runEnv gives
	// them, not a constant.
	env := runEnv(os.Environ())
	f.GOGC = "unset (Go's default, 100)"
	if v, ok := envValue(env, "GOGC"); ok {
		f.GOGC = v
	}
	f.RunEnv = runEnvFact(env)
	f.Commit = output("git", "-c", "safe.directory=*", "-C", root, "rev-parse", "HEAD")
	f.Dirty = output("git", "-c", "safe.directory=*", "-C", root, "status", "--porcelain", "--untracked-files=no") != ""
	if runtime.GOOS == "windows" {
		f.CPU = regValue(output("reg", "query", `HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "/v", "ProcessorNameString"))
		f.Power = firstLine(output("powercfg", "/getactivescheme"))
		f.Defender = output("powershell", "-NoProfile", "-Command", "(Get-MpComputerStatus).RealTimeProtectionEnabled")
		f.OS = "windows " + firstLine(output("cmd", "/c", "ver"))
		f.Busy = busyProcesses(output("tasklist", "/FO", "CSV", "/NH"))
	}
	if runtime.GOOS == "linux" {
		for _, l := range strings.Split(output("cat", "/proc/cpuinfo"), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				f.CPU = strings.TrimSpace(v)
				break
			}
		}
		f.CPU += ", " + output("nproc", "--all") + " CPUs in the VM (" + output("nproc") + " allowed by taskset)"
		f.OS = output("uname", "-sr")
		for _, l := range strings.Split(output("cat", "/etc/os-release"), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				f.OS += ", " + strings.Trim(v, `"`)
			}
		}
		f.Power, f.Defender = "n/a (Linux guest)", "n/a (Linux guest)"
	}
	for lang, path := range map[string]string{"c": t.Clang, "rust": t.Rustc, "zig": t.Zig, "go": t.Go, "cs": t.Dotnet, "hyperfine": t.Hyperfine} {
		if path != "" {
			f.Versions[lang] = t.version(lang, path)
		}
	}
	if t.Mk != "" {
		// The commit the measured binary was built from, which may
		// not be the working tree's (f.Commit).
		f.Versions["mk"] = mkIdentity(t.Mk, t.version("mk", t.Mk))
		if !strings.Contains(f.Versions["mk"], ", LLVM ") && t.Clang != "" {
			f.Versions["mk"] += ", LLVM from " + t.version("c", t.Clang)
		}
	}
	return f
}

// version is the version text of the tool of language lang at path, as the
// facts print it and as the build stamp hashes it (a toolchain
// upgrade at the same path rebuilds every column it built, so the table
// header names the toolchain that built each cell). For "mk" it is `mk
// version`, whose LLVM line names the LLVM mk uses. Each runs once per
// toolchain value.
func (t *toolchain) version(lang, path string) string {
	key := lang + "\x00" + path
	if v, ok := t.versions[key]; ok {
		return v
	}
	var v string
	switch lang {
	case "c":
		v = firstLine(output(path, "--version"))
	case "rust":
		vv := output(path, "-vV")
		v = firstLine(vv)
		for _, l := range strings.Split(vv, "\n") {
			if strings.HasPrefix(l, "LLVM version:") {
				v += ", LLVM " + strings.TrimSpace(strings.TrimPrefix(l, "LLVM version:"))
			}
		}
	case "zig":
		v = "zig " + output(path, "version") + ", " + firstLine(output(path, "cc", "--version"))
	case "go":
		v = output(path, "version")
	case "cs":
		v = "dotnet " + firstLine(outputEnv(dotnetVersionEnv, path, "--version"))
	case "hyperfine":
		v = output(path, "--version")
	case "cargo":
		v = firstLine(output(path, "--version"))
	case "mk":
		v = output(path, "version")
	}
	if t.versions == nil {
		t.versions = map[string]string{}
	}
	t.versions[key] = v
	return v
}

// regValue takes the data of `reg query ... /v Name`.
func regValue(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if i := strings.Index(l, "REG_SZ"); i >= 0 {
			return strings.TrimSpace(l[i+len("REG_SZ"):])
		}
	}
	return ""
}

// busyNames are the programs whose presence makes a timing suspect.
var busyNames = []string{"go.exe", "mk.exe", "clang.exe", "rustc.exe", "zig.exe", "lld-link.exe", "cargo.exe", "dotnet.exe", "ilc.exe", "hyperfine.exe"}

// busyNow is busyProcesses of the processes running now (none outside
// Windows, where the Linux run is pinned inside WSL).
func busyNow() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	return busyProcesses(output("tasklist", "/FO", "CSV", "/NH"))
}

// mergeBusy merges two busyProcesses lists ("go.exe x2"), keeping each
// program once with the larger count, in first-seen order.
func mergeBusy(a, b []string) []string {
	count := map[string]int{}
	var order []string
	for _, e := range append(slices.Clone(a), b...) {
		name, n := e, 1
		if i := strings.LastIndex(e, " x"); i >= 0 {
			if v, err := strconv.Atoi(e[i+2:]); err == nil {
				name, n = e[:i], v
			}
		}
		if _, ok := count[name]; !ok {
			order = append(order, name)
		}
		count[name] = max(count[name], n)
	}
	var out []string
	for _, name := range order {
		out = append(out, name+" x"+strconv.Itoa(count[name]))
	}
	return out
}

// busyProcesses counts the processes of busyNames and of test binaries
// (*.test.exe) in tasklist's CSV.
func busyProcesses(csv string) []string {
	counts := map[string]int{}
	var order []string
	for _, l := range strings.Split(csv, "\n") {
		name, _, _ := strings.Cut(strings.TrimSpace(l), ",")
		name = strings.ToLower(strings.Trim(name, `"`))
		if name == "" {
			continue
		}
		hit := strings.HasSuffix(name, ".test.exe")
		for _, b := range busyNames {
			hit = hit || name == b
		}
		if !hit {
			continue
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}
	var out []string
	for _, n := range order {
		out = append(out, n+" x"+strconv.Itoa(counts[n]))
	}
	return out
}
