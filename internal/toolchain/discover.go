// Why: a build must use one known LLVM, chosen the same way every time and
// never silently by PATH order, and every cache key that depends on it must
// know which one.
//
// What: Options, Source, Tools, Discover, DefaultDirs, BundleMarker,
// Identity and Need.
//
// How: Discover tries, in order, Options.LLVMDir, MK_LLVM, a bundle's
// ../llvm/bin beside the executable, the host's default locations and the
// PATH directories, and takes the first directory holding clang (a
// complete one, with the host's linker, wins within the last two rules).
// Identity hashes the chosen rule, every path, size and version line.

// Package toolchain finds clang, lld-link, ld.lld, lld and llvm-lib by
// absolute path and runs them as processes on LLVM IR, objects and module-definition
// lists. It compiles C only for a package's own sources and the C import
// shim (cc.go), and it never caches: the driver caches what
// it returns. It knows targets only as triple strings.
package toolchain

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"mk/internal/source"
)

// Options says where to look for LLVM. The zero value means the process
// environment, exec.LookPath, the host OS and the host's official LLVM
// locations.
type Options struct {
	LLVMDir  string                       // --llvm; "" when not given
	Getenv   func(string) string          // nil means os.Getenv (tests inject)
	LookPath func(string) (string, error) // nil means exec.LookPath; used only when PATH lists nothing
	GOOS     string                       // "" means runtime.GOOS; tests inject

	// Defaults replaces the official install locations of the default-locations rule (DefaultDirs) when
	// non-nil (tests inject; nil means DefaultDirs(GOOS)).
	Defaults []string
	// VersionLine returns the output of `<clang> --version`. nil runs it
	// through the process runner; tests inject it for fake directories.
	VersionLine func(clang string) (string, error)
	// Executable returns the running executable's path for the bundle rule.
	// nil means os.Executable with symbolic links and junctions resolved
	// (RealPath); tests inject it.
	Executable func() (string, error)
	// CC1Get and CC1Put optionally persist probed `clang -cc1` argv templates
	// across processes (e.g. in the driver's KindLayout store).
	CC1Get func(key source.Hash) ([]byte, bool)
	CC1Put func(key source.Hash, data []byte)
}

// Source is the discovery rule that chose Tools.Dir.
type Source uint8

// The discovery rules, in priority order, with the bundle rule between MK_LLVM
// and the default locations. A Source is never persisted; Identity hashes it.
const (
	NoSource   Source = iota
	FlagDir           // --llvm <dir>
	EnvDir            // MK_LLVM
	BundleDir         // ../llvm/bin beside the running executable's directory
	DefaultDir        // the official install location of the host
	PathDir           // a directory on PATH
)

func (s Source) String() string {
	switch s {
	case FlagDir:
		return "--llvm"
	case EnvDir:
		return "MK_LLVM"
	case BundleDir:
		return "bundle"
	case DefaultDir:
		return "default location"
	case PathDir:
		return "PATH"
	}
	return "none"
}

// Tools is the resolved toolchain. Every path is absolute and lies in Dir,
// the one directory the rules of Discover chose, so tools of two LLVM
// installs are never mixed; a tool absent from Dir is "".
type Tools struct {
	Dir     string // the directory the tools came from
	Found   Source // which rule found it
	Clang   string
	LLDLink string // COFF linker
	LDLLD   string // ELF linker
	// LLVMLib is the import-library tool. It is optional: without it,
	// ImportLib runs the COFF linker in its /lib mode, which writes the
	// same bytes (TestImportLibByLinker).
	LLVMLib string
	// LLD is the one lld executable of a bundle. The linkers run it as `lld
	// -flavor link` or `lld -flavor gnu` when the named linker is absent from
	// Dir.
	LLD     string
	Version Version // clang's, parsed from `clang --version`

	sizes [5]int64 // file sizes of Clang, LLDLink, LDLLD, LLVMLib, LLD; -1 when absent
	cc1   *cc1Cache
}

// MissingError names what was searched and what was missing: the
// directories in search order, and the tool names not found in any.
type MissingError struct {
	Searched []string // directories, in search order
	Missing  []string // tool names
}

func (e *MissingError) Error() string {
	where := "nowhere (no --llvm, MK_LLVM, default location or PATH entry)"
	if len(e.Searched) > 0 {
		where = strings.Join(e.Searched, ", ")
	}
	return fmt.Sprintf("no usable LLVM: %s not found; searched %s", strings.Join(e.Missing, ", "), where)
}

// Tool names without the executable suffix.
const (
	NameClang   = "clang"
	NameLLDLink = "lld-link"
	NameLDLLD   = "ld.lld"
	NameLLVMLib = "llvm-lib"
	NameLLD     = "lld"
)

// DefaultDirs is the default-locations rule for a host OS: `C:\Program Files\LLVM\bin` on
// Windows; on Linux /usr/lib/llvm-N/bin for N from MaxTestedMajor down to
// MinMajor (21 alone), then /usr/local/bin and /usr/bin; on macOS
// Homebrew's llvm (/opt/homebrew/opt/llvm/bin, /usr/local/opt/llvm/bin),
// then /usr/local/bin.
func DefaultDirs(goos string) []string {
	switch goos {
	case "windows":
		return []string{`C:\Program Files\LLVM\bin`}
	case "linux":
		var d []string
		for n := MaxTestedMajor; n >= MinMajor; n-- {
			d = append(d, fmt.Sprintf("/usr/lib/llvm-%d/bin", n))
		}
		return append(d, "/usr/local/bin", "/usr/bin")
	case "darwin":
		// Homebrew's keg-only llvm, on Apple silicon and on Intel; Apple's
		// own clang has no lld.
		return []string{"/opt/homebrew/opt/llvm/bin", "/usr/local/opt/llvm/bin", "/usr/local/bin"}
	}
	return []string{"/usr/local/bin", "/usr/bin"}
}

// Discover resolves the tools. The driver calls it once per process and only
// for native commands. It never lets PATH order choose silently: the chosen
// directory and rule are in Tools, and the rules are, first that yields a
// directory holding clang: (1) Options.LLVMDir, an error when it holds no
// clang; (2) MK_LLVM; (3) a bundle's complete ../llvm/bin beside the running
// executable's bin directory under RELEASE.txt (bundleDir); (4) the host's
// default locations; (5) the PATH directories holding clang. Within rules 4
// and 5 the first complete directory (clang and the host format's linker,
// named or lld) wins over an earlier one holding clang alone. The other tools
// come from the chosen directory only. The error is *MissingError, or
// *VersionError for a clang older than MinMajor (LLVM 21), or the error of
// running `clang --version`.
func Discover(opts Options) (*Tools, error) {
	o := opts.withDefaults()
	var searched []string
	pick := func(dirs []string, src Source) (*Tools, bool) {
		first := ""
		for _, d := range dirs {
			if d == "" {
				continue
			}
			searched = append(searched, d)
			if !isFile(filepath.Join(d, o.exe(NameClang))) {
				continue
			}
			if o.complete(d) {
				return o.tools(d, src), true
			}
			if first == "" {
				first = d
			}
		}
		if first != "" {
			return o.tools(first, src), true
		}
		return nil, false
	}
	var t *Tools
	var ok bool
	if o.LLVMDir != "" {
		if t, ok = pick([]string{abs(o.LLVMDir)}, FlagDir); !ok {
			return nil, &MissingError{Searched: searched, Missing: []string{NameClang}}
		}
	}
	if !ok {
		if env := o.Getenv("MK_LLVM"); env != "" {
			t, ok = pick([]string{abs(env)}, EnvDir)
		}
	}
	if !ok {
		if d := o.bundleDir(); d != "" {
			t, ok = pick([]string{d}, BundleDir)
		}
	}
	if !ok {
		t, ok = pick(o.Defaults, DefaultDir)
	}
	if !ok {
		t, ok = pick(o.pathDirs(), PathDir)
	}
	if !ok {
		return nil, &MissingError{Searched: searched, Missing: []string{NameClang}}
	}
	out, err := o.VersionLine(t.Clang)
	if err != nil {
		return nil, err
	}
	v, err := ParseVersion(out)
	if err != nil {
		return nil, fmt.Errorf("%s --version: %w", t.Clang, err)
	}
	t.Version = v
	if !v.Supported() {
		return nil, &VersionError{Path: t.Clang, Version: v}
	}
	return t, nil
}

type resolved struct{ Options }

func (o Options) withDefaults() resolved {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Defaults == nil {
		o.Defaults = DefaultDirs(o.GOOS)
	}
	if o.VersionLine == nil {
		o.VersionLine = clangVersionLine
	}
	if o.Executable == nil {
		o.Executable = func() (string, error) {
			p, err := os.Executable()
			if err != nil {
				return "", err
			}
			return RealPath(p), nil
		}
	}
	return resolved{o}
}

// BundleMarker is the file one level above a bundle's bin directory
// (RELEASE.txt).
const BundleMarker = "RELEASE.txt"

// bundleDir is the bundle rule's directory: ../llvm/bin relative to the
// directory of the running executable, only when the executable is in a
// bundle: its directory is named bin, RELEASE.txt is one level up, and
// llvm/bin is complete (clang and lld or the host's named linker). Else
// "", and discovery goes on to the default locations, so a stray llvm/bin
// (clang alone, or an old one) beside some mk never stops it.
func (o resolved) bundleDir() string {
	exe, err := o.Executable()
	if err != nil || exe == "" {
		return ""
	}
	bin := filepath.Dir(abs(exe))
	if !strings.EqualFold(filepath.Base(bin), "bin") {
		return ""
	}
	top := filepath.Dir(bin)
	if !isFile(filepath.Join(top, BundleMarker)) {
		return ""
	}
	d := filepath.Join(top, "llvm", "bin")
	if !isFile(filepath.Join(d, o.exe(NameClang))) || !o.complete(d) {
		return ""
	}
	return d
}

func (o resolved) exe(name string) string {
	if o.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// complete reports whether d holds the host format's linker beside clang:
// the named one (lld-link on Windows, ld.lld elsewhere) or lld. llvm-lib is
// not needed: the COFF linker's /lib mode writes the same import libraries
// (ImportLib).
func (o resolved) complete(d string) bool {
	if isFile(filepath.Join(d, o.exe(NameLLD))) {
		return true
	}
	if o.GOOS == "windows" {
		return isFile(filepath.Join(d, o.exe(NameLLDLink)))
	}
	return isFile(filepath.Join(d, o.exe(NameLDLLD)))
}

func (o resolved) tools(d string, src Source) *Tools {
	t := &Tools{Dir: d, Found: src, cc1: newCC1Cache(o.CC1Get, o.CC1Put)}
	ptrs := []*string{&t.Clang, &t.LLDLink, &t.LDLLD, &t.LLVMLib, &t.LLD}
	for i, name := range []string{NameClang, NameLLDLink, NameLDLLD, NameLLVMLib, NameLLD} {
		t.sizes[i] = -1
		p := filepath.Join(d, o.exe(name))
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			*ptrs[i] = p
			t.sizes[i] = fi.Size()
		}
	}
	return t
}

// pathDirs lists the PATH entries in order, made absolute and without
// duplicates. When PATH is empty, the directory LookPath gives for clang.
func (o resolved) pathDirs() []string {
	sep := ":"
	if o.GOOS == "windows" {
		sep = ";"
	}
	var dirs []string
	seen := map[string]bool{}
	for _, d := range strings.Split(o.Getenv("PATH"), sep) {
		d = strings.Trim(d, `"`)
		if d == "" {
			continue
		}
		d = abs(d)
		key := d
		if o.GOOS == "windows" {
			key = strings.ToLower(d)
		}
		if !seen[key] {
			seen[key] = true
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		if p, err := o.LookPath(o.exe(NameClang)); err == nil {
			dirs = append(dirs, filepath.Dir(abs(p)))
		}
	}
	return dirs
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// VersionOutput is the output of `<clang> --version`, run through the
// process runner: what Discover reads when Options.VersionLine is nil, for
// a caller that wraps it (the driver).
func VersionOutput(clang string) (string, error) { return clangVersionLine(clang) }

// clangVersionLine runs `clang --version` through the process runner.
func clangVersionLine(clang string) (string, error) {
	out, err := run(context.Background(), NameClang, clang, []string{"--version"}, "")
	return string(out), err
}

// Identity is the hash of the rule that chose the directory and of every
// path, size and version line in Tools: part of every key whose value
// depends on the toolchain (data layouts, import libraries, object packs),
// never of check keys. The rule is in it so that a cache never mixes the
// outputs of two toolchains.
func (t *Tools) Identity() source.Hash {
	h := source.NewHasher("mk.toolchain.identity.v2")
	h.String(t.Found.String())
	h.String(t.Dir)
	for i, p := range []string{t.Clang, t.LLDLink, t.LDLLD, t.LLVMLib, t.LLD} {
		h.String(p)
		h.Varint(t.sizes[i])
	}
	h.String(t.Version.Line)
	return h.Sum()
}

// Need reports the tools a native build for the triple needs that are
// missing, by name: clang always; lld-link for a Windows triple (whose
// import libraries llvm-lib or the linker's /lib mode writes); ld.lld for a
// Linux or Nutshell triple. lld stands in for either linker.
// Empty means the build can run.
func (t *Tools) Need(triple string) []string {
	var miss []string
	add := func(path, name string) {
		if path == "" {
			miss = append(miss, name)
		}
	}
	add(t.Clang, NameClang)
	if t.LLD != "" {
		return miss
	}
	switch OSOf(triple) {
	case "windows":
		add(t.LLDLink, NameLLDLink)
	case "linux":
		add(t.LDLLD, NameLDLLD)
	}
	if IsNutshellTriple(triple) {
		add(t.LDLLD, NameLDLLD)
	}
	return miss
}

// OSOf is the OS component of a triple as a GOOS name ("windows", "linux",
// "darwin"), or "" when the triple names none of them.
func OSOf(triple string) string {
	for _, part := range strings.Split(triple, "-")[1:] {
		switch {
		case strings.HasPrefix(part, "windows"):
			return "windows"
		case strings.HasPrefix(part, "linux"):
			return "linux"
		case strings.HasPrefix(part, "darwin"), strings.HasPrefix(part, "macos"):
			return "darwin"
		}
	}
	return ""
}

// ArchOf is the architecture component of a triple as a GOARCH name
// ("amd64", "arm64"), or "" for any other.
func ArchOf(triple string) string {
	arch, _, _ := strings.Cut(triple, "-")
	switch arch {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return ""
}

// Describe is the human-readable discovery report `mk version` prints and
// E0001 cites: the rule, the directory and each tool's path.
func (t *Tools) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "LLVM %s from %s (%s)\n", t.Version, t.Dir, t.Found)
	for _, kv := range [][2]string{{NameClang, t.Clang}, {NameLLDLink, t.LLDLink}, {NameLDLLD, t.LDLLD}, {NameLLVMLib, t.LLVMLib}, {NameLLD, t.LLD}} {
		p := kv[1]
		if p == "" {
			p = "(absent)"
		}
		fmt.Fprintf(&b, "  %-8s %s\n", kv[0], p)
	}
	return b.String()
}
