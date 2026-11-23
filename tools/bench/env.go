// Why: tests/bench/README.md says "Go then runs with GOMAXPROCS 1 and GOGC
// 100". A timed program that inherited the caller's environment would let a
// GOGC=off or a GODEBUG in the shell change the Go column unseen while the
// facts still said "GOGC 100". And a published number must name the mk
// binary it measured.
//
// What: runEnv and rowEnv, the environment of every program tools/bench
// runs; mkIdentity, the identity of the mk binary measured; and
// refuseExperiment, which keeps an experimental mk out of published tables.
//
// How: every run (verify, counting, hyperfine, compare, codegen) gets
// runEnv: the runtime knobs below cleared, then GOGC and GOMAXPROCS pinned.
// GOMAXPROCS 1 is what the one-CPU affinity gives Go anyway; pinning it
// makes it hold in compare and verify runs too.

package main

import (
	"cmp"
	"debug/buildinfo"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
)

// runEnvSet is what every program gets set.
var runEnvSet = []string{"GOGC=100", "GOMAXPROCS=1"}

// runEnvClear is what every program gets unset: Go's other runtime knobs and
// the Mk runtime's (std/task/start.mk MK_CARRIERS and MK_IO, std/rt/profile.mk
// MK_PROFILE_DIR, std/proc MK_INHERIT), plus every MK_TEST_ variable.
var runEnvClear = []string{"GODEBUG", "GOMEMLIMIT", "GOTRACEBACK", "MK_CARRIERS", "MK_IO", "MK_PROFILE_DIR", "MK_INHERIT"}

const runEnvClearPrefix = "MK_TEST_"

// envKeyIs reports whether the entry kv names key. Windows variable names
// are case-insensitive, so a stray "gogc=off" is GOGC there.
func envKeyIs(kv, key string) bool {
	k, _, _ := strings.Cut(kv, "=")
	if runtime.GOOS == "windows" {
		return strings.EqualFold(k, key)
	}
	return k == key
}

func envKeyHasPrefix(kv, prefix string) bool {
	k, _, _ := strings.Cut(kv, "=")
	if runtime.GOOS == "windows" {
		return len(k) >= len(prefix) && strings.EqualFold(k[:len(prefix)], prefix)
	}
	return strings.HasPrefix(k, prefix)
}

// runEnv is base with the variables of runEnvClear, csRuntimeUnset and
// runEnvSet removed and runEnvSet appended.
func runEnv(base []string) []string {
	out := slices.DeleteFunc(slices.Clone(base), func(kv string) bool {
		if envKeyHasPrefix(kv, runEnvClearPrefix) {
			return true
		}
		for _, k := range runEnvClear {
			if envKeyIs(kv, k) {
				return true
			}
		}
		for _, k := range csRuntimeUnset {
			if p, ok := strings.CutSuffix(k, "*"); ok {
				if envKeyHasPrefix(kv, p) {
					return true
				}
			} else if envKeyIs(kv, k) {
				return true
			}
		}
		for _, s := range runEnvSet {
			k, _, _ := strings.Cut(s, "=")
			if envKeyIs(kv, k) {
				return true
			}
		}
		return false
	})
	return append(out, runEnvSet...)
}

// cellEnv is rowEnv(r) plus any column-specific runtime pins (RyuJIT's ISA
// pinning for the cs column so it matches the AOT columns' CPU target).
func cellEnv(r *row, c column) []string {
	env := rowEnv(r)
	if extra := csRunEnv(r, c); len(extra) > 0 {
		env = append(env, extra...)
	}
	return env
}

// rowEnv is the environment of row r's programs: runEnv, and for a
// concurrency row GOMAXPROCS and MK_CARRIERS set to its thread count,
// so the Go and Mk runtimes get the same number of threads as the CPU set
// the row runs on.
func rowEnv(r *row) []string {
	env := runEnv(os.Environ())
	if r == nil {
		return env
	}
	if r.Concurrency != nil {
		env = slices.DeleteFunc(env, func(kv string) bool { return envKeyIs(kv, "GOMAXPROCS") })
		n := strconv.Itoa(r.Concurrency.Threads)
		env = append(env, "GOMAXPROCS="+n, "MK_CARRIERS="+n)
	}
	// An app row's env (checkApp refuses the variables above), in key
	// order, each replacing any inherited value.
	for _, k := range slices.Sorted(maps.Keys(r.Env)) {
		env = slices.DeleteFunc(env, func(kv string) bool { return envKeyIs(kv, k) })
		env = append(env, k+"="+r.Env[k])
	}
	return env
}

// envValue is the value of key in env, and whether it is set.
func envValue(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if envKeyIs(env[i], key) {
			_, v, _ := strings.Cut(env[i], "=")
			return v, true
		}
	}
	return "", false
}

// runEnvFact is the facts' line for the programs' environment, read back
// from what runEnv gives them.
func runEnvFact(env []string) string {
	var set []string
	for _, s := range runEnvSet {
		k, _, _ := strings.Cut(s, "=")
		if v, ok := envValue(env, k); ok {
			set = append(set, k+"="+v)
		}
	}
	return strings.Join(set, " ") + " set by tools/bench on every program it runs; " +
		strings.Join(runEnvClear, ", ") + " and " + runEnvClearPrefix + "* cleared"
}

// mkIdentity names the mk binary at path by the commit it was built from,
// read from the build information Go embeds in it, not by the commit of
// the working tree. versionText is `mk version`'s output, whose
// LLVM line names the LLVM that mk uses, and its "experiment:" line (a
// binary built with a compiler experiment) is kept after it, so such a
// binary is never named as a default one.
func mkIdentity(path, versionText string) string {
	id := "mk with no VCS revision in the binary (its sha256 identifies it)"
	if bi, err := buildinfo.ReadFile(path); err == nil {
		id = mkIdentityFrom(bi.Settings)
	}
	for _, l := range strings.Split(versionText, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "LLVM ") {
			id += ", " + l
			break
		}
	}
	if e := mkExperiment(versionText); e != "" {
		id += ", " + e
	}
	return id
}

// mkExperiment is the "experiment:" line of `mk version`'s text or of an
// identity mkIdentity made from it, or "" for a default binary. A binary
// with a bad baked value prints its error on that line, and counts too.
func mkExperiment(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "experiment:") {
			return l
		}
	}
	if _, e, ok := strings.Cut(text, ", experiment:"); ok {
		return "experiment:" + e
	}
	return ""
}

// refuseExperiment is every publish path's check: an mk binary built with an
// experiment measures that experiment, not Mk, so publish, publish-linux,
// codegen -publish, compile -publish and http -publish refuse it before they
// take the machine lock; compare, ir, and codegen, compile and http without
// -publish take it. compile-linux's Linux mk cannot run here, so
// refuseELFExperiment reads the same setting out of the ELF before the lock
// instead. mk is the binary's path ("" for none); identity is an mkIdentity
// handed over by another process (publish -mk-version, from publish-linux),
// or "". The check fails closed: with neither, or when `mk version` gives no
// "mk build" line (it did not run, or printed something else), nothing says
// the binary is a default one, and it is refused.
func refuseExperiment(mk, identity string) error {
	if mk == "" && identity == "" {
		return errors.New("refusing to publish: neither an mk binary nor an -mk-version identity to check for a tuning experiment")
	}
	e := mkExperiment(identity)
	if e == "" && mk != "" {
		v := output(mk, "version")
		if !hasBuildLine(v) {
			return fmt.Errorf("refusing to publish: cannot read %s's `mk version` (no \"mk build\" line), so it cannot be checked for a tuning experiment", mk)
		}
		e = mkExperiment(v)
	}
	if e != "" {
		return fmt.Errorf("refusing to publish: %s was built with a tuning experiment (%s); a published table measures a default build only; compare, ir, and codegen, compile or http without -publish take it", cmp.Or(mk, "the mk binary"), e)
	}
	return nil
}

// hasBuildLine reports whether versionText has `mk version`'s "mk build"
// line (driver.VersionText's second line; v1.1.0 and v1.2.0 print it).
func hasBuildLine(versionText string) bool {
	for _, l := range strings.Split(versionText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "mk build ") {
			return true
		}
	}
	return false
}

// mkIdentityFrom is mkIdentity's reading of the build settings.
func mkIdentityFrom(settings []debug.BuildSetting) string {
	var rev, modified, when string
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		case "vcs.time":
			when = s.Value
		}
	}
	if rev == "" {
		return "mk with no VCS revision in the binary (its sha256 identifies it)"
	}
	s := "mk built from " + rev
	if when != "" {
		s += " (" + when + ")"
	}
	if modified == "true" {
		s += " with uncommitted changes"
	}
	return s
}
