// Why: a bug report needs to say exactly which mk ran and which LLVM it
// would use.
//
// What: Release, Commit and VersionText, the text of `mk version`.
//
// How: the release constant, the commit (a link-time override, else Go's
// VCS stamp), mk's build ID, the Go version, and the toolchain
// discovery report. cmd/mk reaches toolchain only through the driver.

package driver

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"mk/internal/toolchain"
)

// Release is the version of Mk this compiler implements: the tag it ships
// at.
const Release = "0.6.0"

// commit overrides the VCS stamp when set at link time
// (-ldflags "-X mk/internal/driver.commit=<hash>"), for a build made outside
// the repository.
var commit string

// Commit is the commit mk was built from: the link-time override, else the
// Go toolchain's VCS stamp (vcs.revision, 12 hex digits, "+dirty" when the
// tree had changes), else "unknown" (go run and go test carry no stamp).
func Commit() string {
	if commit != "" {
		return commit
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return commitOf(bi.Settings)
}

// commitOf reads the VCS stamp out of a build's settings.
func commitOf(settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}

// VersionText is what `mk version` prints: the release and the commit, the
// build ID (16 hex digits), the Go version and target, then the toolchain
// discovery report, or why no LLVM was found (mk check works without it).
func VersionText(llvmDir string) string {
	var b strings.Builder
	id := BuildID()
	fmt.Fprintf(&b, "mk %s commit %s\n", Release, Commit())
	fmt.Fprintf(&b, "mk build %s (%s %s/%s)\n", id.String()[:16], runtime.Version(), runtime.GOOS, runtime.GOARCH)
	// tools/release build compares this with the bundle's clang.
	fmt.Fprintf(&b, "llvm tested %d (supported %d or newer)\n", toolchain.MaxTestedMajor, toolchain.MinMajor)
	b.WriteString(sysViewVersionLine()) // sdkinfo.go
	// A binary built with a toolchain experiment baked in says so; a
	// default binary prints nothing here.
	if t, err := toolchain.Baked(); err != nil {
		fmt.Fprintf(&b, "experiment: %v\n", err)
	} else if !t.IsZero() {
		fmt.Fprintf(&b, "experiment: %s (never a default build)\n", t)
	}
	tools, err := toolchain.Discover(Config{LLVMDir: llvmDir}.toolchainOptions())
	if err != nil {
		fmt.Fprintf(&b, "toolchain: %v\n", err)
		return b.String()
	}
	b.WriteString(tools.Describe())
	return b.String()
}
