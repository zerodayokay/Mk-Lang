// Why: the link decides what the executable holds, so it must be the same
// bytes from any directory and machine, and every format and mode (COFF,
// ELF, system-libc, freestanding, Nutshell, libraries, LTO, --debug) needs
// its own exact command line.
//
// What: Format, LinkJob, Link and linkArgs, thinLink (distributed ThinLTO
// for COFF) with ThinCacheKey, pdbLink, loadConfigObject, LinkStub and
// StagedArchive.
//
// How: inputs are passed as absolute paths in the driver's order, or staged
// into a private directory under fixed names when the linker would record
// their paths (ThinLTO index links, PDB links), so outputs do not depend on
// where the build ran. COFF ThinLTO runs its backends through clang, whose
// pipeline includes the SLP vectorizer lld-link's own backend lacks.

package toolchain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mk/internal/source"
)

// Format is the object and executable format of a link.
type Format uint8

// The formats; the zero value is no format and is refused.
const (
	NoFormat Format = iota
	COFF
	ELF
	// MachO is macOS's format, linked by ld64.lld (macho.go).
	MachO
)

func (f Format) String() string {
	switch f {
	case COFF:
		return "COFF"
	case ELF:
		return "ELF"
	case MachO:
		return "Mach-O"
	}
	return "none"
}

// FormatOf is the format of a triple's executables: COFF for Windows, ELF
// for Linux, for a triple with no OS (x86_64-unknown-none-elf, the
// freestanding target, free.go) and for Nutshell's (nutshell.go), Mach-O
// for macOS, NoFormat otherwise.
func FormatOf(triple string) Format {
	if isFreeTriple(triple) || IsNutshellTriple(triple) {
		return ELF
	}
	switch OSOf(triple) {
	case "windows":
		return COFF
	case "linux":
		return ELF
	case "darwin":
		return MachO
	}
	return NoFormat
}

// LinkJob is one executable. Every path is absolute; Objects are in the
// order the driver sorted them, so the link is deterministic.
type LinkJob struct {
	Format  Format
	Output  string
	Objects []string // absolute, in the order the driver sorted them
	Libs    []string // import libraries (COFF), or libSystem's stub (Mach-O), absolute
	Entry   string   // "" means "mk_start" (COFF), "_start" (ELF) or "_main" (Mach-O)
	// MachOArch is a Mach-O link's -arch, "arm64" or "x86_64"
	// (MachOArch of the triple); any other link takes "".
	MachOArch    string
	StackReserve uint64 // bytes; 0 means the linker default (COFF /stack only)
	Release      bool   // /opt:ref,icf or --gc-sections
	LTO          bool   // ThinLTO at -O3 over bitcode objects
	LTOCacheDir  string // cache.Store.Dir("thinlto"); "" disables
	LTOCacheSize int64  // bytes, the ThinLTO pruning bound; 0 with LTOCacheDir is an error
	Jobs         int    // ThinLTO backend jobs; 0 means the linker default
	// SectionAlign is the COFF image's section alignment, /align:N, for
	// static data aligned over a page (codegen.StaticAlign); 0 is
	// the linker's 4096. An ELF link takes 0 only.
	SectionAlign uint32
	// Archives are the static archives a package's links name, absolute, in
	// the driver's order: .lib on COFF, .a on ELF. They follow the objects and
	// come before every import library, so a name an archive defines is taken
	// from it. A freestanding link takes none.
	Archives []string
	// InProcessLTO runs a COFF ThinLTO link's backends inside lld-link, as
	// ld.lld runs ELF's, instead of through clang (thinLink, the default).
	// lld-link's backend has no SLP vectorizer (below), so this is for
	// measurement and comparison only.
	InProcessLTO bool
	// Tune is the build's pass-pipeline and LTO experiment (tuning.go); the
	// zero value, every default build's, changes no argument. It acts only
	// with LTO: its LLVM options go to every LTO step, a non-zero Tune drops
	// lld's own ThinLTO cache (whose key does not hash -mllvm options), and
	// FatLTO links in process, never through thinLink.
	Tune Tuning
	// Debug is mk build --debug: COFF writes a PDB beside the executable
	// (/debug:full, lld's default name <out>.pdb) and records only its base
	// name in the image (/pdbaltpath:%_PDB%), so the executable does not
	// depend on the output directory; /Brepro makes the PDB's GUID a hash
	// of the image. ELF needs nothing: ld.lld keeps .debug_* sections, and
	// no strip flag is passed; a release link without Debug passes
	// --strip-all.
	Debug bool
	// Free is a freestanding ELF link (free.go): --no-pie, -z norelro, the
	// entry MkEntry by default, and Script.
	Free bool
	// Script is the absolute path of the bin's linker script (pkg.Bin's
	// link_script), passed as -T; "" for lld's default layout. Only
	// a Free link takes one.
	Script string
	// Nutshell is an x86_64-nutshell link (nutshell.go): the hosted
	// ELF line with NutshellLinkFlags before the output.
	Nutshell bool
	// Subsystem is pkg.Bin's subsystem: "" or "console"
	// links COFF with /subsystem:console, "windows" with /subsystem:windows,
	// a GUI program that opens no console. An ELF link takes "" or
	// "console" only; any other value is a job error.
	Subsystem string
	// DependentLoadFlags, when not 0, is a COFF image's load configuration
	// DependentLoadFlags: the import-table DLLs are searched only where the
	// flags say (0xA00: the executable's directory and System32,
	// never the current directory or PATH). Link adds an object defining
	// _load_config_used (loadConfigObject), which a program with no C
	// runtime lacks and without which lld-link ignores the flag. 0 adds
	// nothing. An ELF link takes 0 only.
	DependentLoadFlags uint16
	// Interp, when not "", is an ELF link in system-libc mode: a dynamically
	// linked, non-PIE executable with this PT_INTERP, -z now, -z relro and
	// --hash-style=gnu, linked against Shared, the stub shared objects
	// (LinkStub), after the objects and archives. Shared[0] is libc.so.6's
	// stub, always needed; the others are --as-needed, so a library whose names
	// the program does not use gets no DT_NEEDED. "" is the static line,
	// unchanged.
	Interp string
	Shared []string
	// VersionScript is an Interp link's version script, absolute, and an
	// Interp link needs one: the driver's makes every symbol the
	// executable defines local ({ local: *; }), so the executable exports
	// nothing. Without it ld.lld exports a definition that a
	// stub also defines, and the loader then binds every library's
	// reference to the executable's copy. Any other link takes
	// "". A Dynamic ELF link takes one too: the library's export list
	// (library.go).
	VersionScript string
	// Kind is the output: Executable, the zero value, or Dynamic, a
	// DLL or shared object (library.go). Static outputs are archives,
	// written by StaticLib, never linked.
	Kind OutputKind
	// ImpLib and Def are a Dynamic COFF link's import library to write
	// (/implib) and its export list (/def), absolute; Soname is a Dynamic
	// ELF link's soname (lib<name>.so.<abi>).
	ImpLib, Def, Soname string
}

// Link runs lld-link (COFF) or ld.lld (ELF) directly, never the clang
// driver, so no C runtime or library is looked for. A COFF link with LTO
// runs ThinLTO's backends through clang (thinLink: lld-link's own backend
// runs no SLP vectorizer) unless InProcessLTO. The error is *ToolError, or
// a job error before any process starts.
func (t *Tools) Link(ctx context.Context, j LinkJob) error {
	if j.Format == COFF && j.DependentLoadFlags != 0 {
		if _, err := linkArgs(j); err != nil {
			return err
		}
		dir, err := os.MkdirTemp(filepath.Dir(j.Output), "mklc-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		obj := filepath.Join(dir, "loadcfg.obj")
		if err := os.WriteFile(obj, loadConfigObject(), 0o644); err != nil {
			return err
		}
		j.Objects = append(append([]string(nil), j.Objects...), obj)
	}
	if thinRoute(j) {
		if _, err := linkArgs(j); err != nil {
			return err
		}
		return t.thinLink(ctx, j)
	}
	args, err := linkArgs(j)
	if err != nil {
		return err
	}
	if j.Format == COFF && j.Debug {
		return t.pdbLink(ctx, j)
	}
	_, err = t.runLinker(ctx, j.Format, args, filepath.Dir(j.Output))
	return err
}

// thinRoute reports whether Link takes j through thinLink: a COFF LTO link,
// unless InProcessLTO or the fatlto experiment (one module, lld-link's own
// full LTO).
func thinRoute(j LinkJob) bool {
	return j.Format == COFF && j.LTO && !j.InProcessLTO && !j.Tune.FatLTO
}

// ltoLoopAlign is the loop alignment of the LTO code generator: 32 bytes
// (lld takes it as an LLVM option; clang, for thinLink's backends, only as
// its driver flag -falign-loops=32, so thinBackendArgs says that),
// so a small hot loop never straddles a 32- or 64-byte fetch line. LLVM's
// x86 default is 16. Measured on a Zen 5 machine: the same intsum Main,
// its 17-byte sum loop moved by an unrelated change in another object from
// 0x...e90 to 0x...ef0 (across a 64-byte line), ran 5 to 25 percent slower;
// aligned to 32 it ran as fast as the original or faster. The same holds
// for every release link.
const ltoLoopAlign = "-align-loops=32"

// ltoUnroll is the full-unroll threshold of the LTO optimizer, given to
// every ThinLTO backend (lld's as an LLVM option, thinLink's clang through
// -mllvm): 400, where LLVM's -O3 default is 300. The default leaves a small
// nest of constant trip count rolled when its unrolled body is just above
// 300, as nbody's pair loop (5 bodies, 10 pairs) is in Mk and in C alike;
// fully unrolled, every index is a constant, the state lives in registers
// and the SLP vectorizer pairs the iterations: nbody's pair loop unrolls
// from a threshold of 350, and unrolled it runs well ahead of the rolled
// loop.
const ltoUnroll = "-unroll-threshold=400"

// linkArgs is the linker command line after the executable.
//
// COFF: /nologo /nodefaultlib /entry:E /subsystem:S (console unless Subsystem)
// /osversion:10.0 /machine:x64 /Brepro /out:OUT [/dependentloadflag:F] OBJS
// ARCHIVES LIBS [/opt:ref /opt:icf] [/stack:N] [/align:A] [/opt:lldlto=3
// [/lldltocache:DIR /lldltocachepolicy:cache_size_bytes=B:prune_after=48h]
// [/opt:lldltojobs=J]] [/debug:full /pdbaltpath:%_PDB%]. No /debug unless
// Debug, so a plain build writes no PDB.
//
// ELF: -static -nostdlib -e E --build-id=none -z noexecstack -o OUT OBJS ARCHIVES
// (system-libc mode, Interp: -nostdlib -e E --build-id=none -z noexecstack
// --dynamic-linker=I -z now -z relro --hash-style=gnu --version-script=V
// -o OUT OBJS ARCHIVES --no-as-needed LIBC --as-needed SHARED...)
// [--gc-sections] [--lto-O3 [--thinlto-cache-dir=DIR
// --thinlto-cache-policy=cache_size_bytes=B:prune_after=48h]
// [--thinlto-jobs=J]].
func linkArgs(j LinkJob) ([]string, error) {
	if !filepath.IsAbs(j.Output) {
		return nil, fmt.Errorf("toolchain: link output must be absolute: %q", j.Output)
	}
	if len(j.Objects) == 0 {
		return nil, errors.New("toolchain: link job without objects")
	}
	for _, p := range append(append(append(append([]string(nil), j.Objects...), j.Libs...), j.Archives...), j.Shared...) {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("toolchain: link input must be absolute: %q", p)
		}
	}
	// LTOCacheSize 0 with LTOCacheDir is an error, whether or not
	// this link uses LTO.
	if j.LTOCacheDir != "" && j.LTOCacheSize <= 0 {
		return nil, errors.New("toolchain: a ThinLTO cache directory needs a positive LTOCacheSize")
	}
	if j.LTOCacheSize < 0 {
		return nil, errors.New("toolchain: negative LTOCacheSize")
	}
	if j.LTOCacheDir != "" && !filepath.IsAbs(j.LTOCacheDir) {
		return nil, fmt.Errorf("toolchain: ThinLTO cache directory must be absolute: %q", j.LTOCacheDir)
	}
	policy := "cache_size_bytes=" + strconv.FormatInt(j.LTOCacheSize, 10) + ":prune_after=48h"
	subsystem := "console"
	switch j.Subsystem {
	case "", "console":
	case "windows":
		if j.Format != COFF {
			return nil, errors.New("toolchain: subsystem \"windows\" is for a COFF link")
		}
		subsystem = "windows"
	default:
		return nil, fmt.Errorf("toolchain: subsystem %q is not \"console\" or \"windows\"", j.Subsystem)
	}
	if j.Kind == Dynamic {
		return dynamicArgs(j, policy)
	}
	if j.Format == MachO {
		return machoArgs(j)
	}
	if j.Kind != Executable {
		return nil, fmt.Errorf("toolchain: a link makes an executable or a dynamic library, not %v", j.Kind)
	}
	if j.ImpLib != "" || j.Def != "" || j.Soname != "" {
		return nil, errors.New("toolchain: ImpLib, Def and Soname are for a dynamic library")
	}
	switch j.Format {
	case COFF:
		entry := j.Entry
		if entry == "" {
			entry = "mk_start"
		}
		// /osversion:10.0: the header names Windows 10, which mk
		// needs anyway, so Windows answers version queries without
		// its compatibility shim; the subsystem version stays lld-link's 6.0.
		args := []string{"/nologo", "/nodefaultlib", "/entry:" + entry, "/subsystem:" + subsystem, "/osversion:10.0",
			"/machine:x64", "/Brepro", "/out:" + j.Output}
		if j.DependentLoadFlags != 0 {
			args = append(args, fmt.Sprintf("/dependentloadflag:0x%X", j.DependentLoadFlags))
		}
		args = append(args, j.Objects...)
		args = append(args, j.Archives...)
		args = append(args, j.Libs...)
		if j.LTO {
			for _, o := range j.Tune.ltoOptions() {
				args = append(args, "/mllvm:"+o)
			}
		}
		if j.Release {
			args = append(args, "/opt:ref", "/opt:icf")
		} else {
			// lld-link folds identical code by default when there
			// is no /debug; a dev link skips that work. It keeps lld-link's
			// default /opt:ref, or /opt:noref with /debug, where /opt:noicf
			// restates the default.
			args = append(args, "/opt:noicf")
		}
		if j.StackReserve > 0 {
			args = append(args, "/stack:"+strconv.FormatUint(j.StackReserve, 10))
		}
		if j.SectionAlign > 0 {
			args = append(args, "/align:"+strconv.FormatUint(uint64(j.SectionAlign), 10))
		}
		if j.LTO {
			args = append(args, "/opt:lldlto=3")
			if j.LTOCacheDir != "" && j.Tune.IsZero() {
				args = append(args, "/lldltocache:"+j.LTOCacheDir, "/lldltocachepolicy:"+policy)
			}
			if j.Jobs > 0 && !j.Tune.FatLTO {
				args = append(args, "/opt:lldltojobs="+strconv.Itoa(j.Jobs))
			}
		}
		if j.Debug {
			args = append(args, "/debug:full", "/pdbaltpath:%_PDB%")
		}
		return args, nil
	case ELF:
		if j.DependentLoadFlags != 0 {
			return nil, errors.New("toolchain: DependentLoadFlags is COFF only")
		}
		if j.SectionAlign != 0 {
			return nil, errors.New("toolchain: SectionAlign is COFF only")
		}
		if j.Interp != "" && (j.Free || j.Nutshell || len(j.Shared) == 0) {
			return nil, errors.New("toolchain: a system-libc link is hosted x86_64-linux with libc.so.6's stub")
		}
		if j.Interp == "" && (len(j.Shared) > 0 || j.VersionScript != "") {
			return nil, errors.New("toolchain: shared objects and a version script need a system-libc link (Interp)")
		}
		if j.Interp != "" && !filepath.IsAbs(j.VersionScript) {
			return nil, fmt.Errorf("toolchain: a system-libc link needs an absolute version script: %q", j.VersionScript)
		}
		if j.Free {
			if len(j.Archives) > 0 {
				return nil, errors.New("toolchain: a freestanding link takes no archives")
			}
			return freeLinkArgs(j, policy)
		}
		if j.Script != "" {
			return nil, errors.New("toolchain: a linker script needs a freestanding link")
		}
		if len(j.Libs) > 0 {
			return nil, errors.New("toolchain: import libraries are COFF only")
		}
		entry := j.Entry
		if entry == "" {
			entry = "_start"
		}
		args := []string{"-static", "-nostdlib", "-e", entry, "--build-id=none", "-z", "noexecstack"}
		if j.Interp != "" {
			args = []string{"-nostdlib", "-e", entry, "--build-id=none", "-z", "noexecstack",
				"--dynamic-linker=" + j.Interp, "-z", "now", "-z", "relro", "--hash-style=gnu", "--version-script=" + j.VersionScript}
		}
		if j.Nutshell {
			args = append(args, NutshellLinkFlags...)
		}
		args = append(args, "-o", j.Output)
		args = append(args, j.Objects...)
		args = append(args, j.Archives...)
		if j.Interp != "" {
			args = append(append(args, "--no-as-needed", j.Shared[0], "--as-needed"), j.Shared[1:]...)
		}
		if j.LTO {
			args = append(args, j.Tune.mllvm()...)
		}
		if j.Release {
			args = append(args, "--gc-sections")
			// A release image without --debug carries no symbol or
			// debug tables, as a COFF one without /debug writes no PDB; the
			// objects keep their line tables, and panics print
			// positions from mk's own panic sites, not from DWARF.
			if !j.Debug {
				args = append(args, "--strip-all")
			}
		}
		if j.LTO {
			args = append(args, "--lto-O3")
			if j.LTOCacheDir != "" && j.Tune.IsZero() {
				args = append(args, "--thinlto-cache-dir="+j.LTOCacheDir, "--thinlto-cache-policy="+policy)
			}
			if j.Jobs > 0 && !j.Tune.FatLTO {
				args = append(args, "--thinlto-jobs="+strconv.Itoa(j.Jobs))
			}
		}
		return args, nil
	}
	return nil, fmt.Errorf("toolchain: unknown link format %d", j.Format)
}

// Distributed ThinLTO for COFF.
//
// lld-link's in-process ThinLTO backend leaves the pass pipeline's SLP
// vectorizer off: lld's COFF LTO configuration does not set
// PipelineTuningOptions.SLPVectorization, which is false by default, while
// ld.lld sets it at O2 and above, and no lld-link option reaches it
// (/mllvm:-vectorize-slp only gates a pass that is not in the pipeline).
// Measured with clang and lld 21.1.8: a Windows release nbody had no
// mulpd, the Linux one 121. clang's own ThinLTO backend
// (-fthinlto-index) sets the tuning options from its flags, and the clang
// driver turns on -vectorize-slp at -O3, so its pipeline is ld.lld's.
//
// thinLink is therefore LLVM's distributed ThinLTO, as build systems run
// it: (1) lld-link /thinlto-index-only writes each bitcode module's index
// and import list; (2) one clang per module compiles it with its index to a
// native object, at most Jobs at a time; (3) lld-link links the native
// objects. The bitcode modules are hard-linked (or copied) into a private
// directory under fixed names and the index link runs there with relative
// names, so the index files do not depend on where the build ran and the
// backend cache below can hit.
//
// The backend cache is LTOCacheDir: mkdt-<sha256>.o, keyed by clang's
// version line, the backend command line, the module's bytes, its index
// file's bytes and the bytes of every module its import list names, which
// is everything the backend reads. A hit is the cached object (touched); a
// miss writes it through a temporary file and a rename. After the link the
// mkdt- entries are pruned to LTOCacheSize, oldest first, and of entries
// older than 48 hours, the policy given to lld for its own cache.

// bitcodeMagic and bitcodeWrapper are the first bytes of an LLVM bitcode
// file, raw or in the wrapper header.
var (
	bitcodeMagic   = []byte{'B', 'C', 0xC0, 0xDE}
	bitcodeWrapper = []byte{0xDE, 0xC0, 0x17, 0x0B}
)

// isBitcode reports whether the file at path starts as LLVM bitcode.
func isBitcode(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var b [4]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return false, nil
	}
	return bytes.Equal(b[:], bitcodeMagic) || bytes.Equal(b[:], bitcodeWrapper), nil
}

// thinTriple is the triple the backends compile for. linkArgs links only
// x64 COFF (/machine:x64), and the backend takes its CPU from each
// function's "target-cpu" attribute, as lld-link's backend does.
const thinTriple = windowsMSVCTriple

// thinBackendArgs is the clang command line of one backend, run in the
// module's directory: the codegen flags of compileArgs for COFF, at -O3,
// then the loop alignment and t's backend options (for the zero Tuning,
// ltoUnroll alone).
func thinBackendArgs(module, index, out string, t Tuning) []string {
	args := []string{ClangTargetArg(thinTriple), "-O3", "-x", "ir", module, "-fthinlto-index=" + index,
		"-c", "-o", out, "-Wno-override-module", "-ffunction-sections", "-fdata-sections", "-fno-addrsig",
		"-fdebug-compilation-dir=.", "-mno-incremental-linker-compatible", "-Xclang", "-gno-codeview-command-line",
		"-falign-loops=32"}
	for _, o := range t.backendOptions() {
		args = append(args, "-mllvm", o)
	}
	return args
}

// ThinCacheKey is the cache key of one distributed ThinLTO backend
// (thinLink): everything the backend reads, so a changed input never meets
// a stale object. versionLine is clang's first --version line and args the
// backend's command line; read returns a file's bytes by name (module, its
// index file, and each module the imports text names); imports is the
// module's .imports file, one module name per line, as lld-link writes it.
// The SHA-256, in hex, of each input with its tag and length. Exported only
// for internal/codegen's TestDTLTOCacheKey (a test-only import edge).
func ThinCacheKey(versionLine string, args []string, module, index string, imports []byte, read func(string) ([]byte, error)) (string, error) {
	h := sha256.New()
	put := func(tag string, b []byte) {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(b)))
		h.Write([]byte(tag))
		h.Write(n[:])
		h.Write(b)
	}
	put("version", []byte(versionLine))
	put("args", []byte(strings.Join(args, "\x00")))
	for _, p := range []string{module, index} {
		b, err := read(p)
		if err != nil {
			return "", err
		}
		put("file", b)
	}
	put("imports", imports)
	for _, name := range strings.Split(strings.ReplaceAll(string(imports), "\r", ""), "\n") {
		if name == "" {
			continue
		}
		b, err := read(name)
		if err != nil {
			return "", err
		}
		put("import", b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// thinLink is a COFF ThinLTO link through clang's backend (above).
func (t *Tools) thinLink(ctx context.Context, j LinkJob) error {
	work, err := os.MkdirTemp(filepath.Dir(j.Output), "mkdt-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	plain := j
	plain.LTO = false
	// The bitcode modules, each under a fixed name in work.
	var modules []string
	names := make([]string, len(j.Objects)) // "" for a native object
	for i, o := range j.Objects {
		bc, err := isBitcode(o)
		if err != nil {
			return err
		}
		if !bc {
			continue
		}
		name := fmt.Sprintf("m%04d.o", len(modules))
		if err := linkOrCopy(o, filepath.Join(work, name)); err != nil {
			return err
		}
		modules = append(modules, name)
		names[i] = name
	}
	if len(modules) == 0 {
		// No bitcode: one plain link. With Debug it goes through pdbLink
		// as the final link below does, so the image and PDB do not depend
		// on the output directory.
		if j.Debug {
			return t.pdbLink(ctx, plain)
		}
		args, err := linkArgs(plain)
		if err != nil {
			return err
		}
		_, err = t.runLinker(ctx, COFF, args, filepath.Dir(j.Output))
		return err
	}
	// (1) The index link.
	iargs, err := thinIndexArgs(plain, work, names)
	if err != nil {
		return err
	}
	if _, err := t.runLinkerIn(ctx, COFF, iargs, work, true); err != nil {
		return err
	}
	// (2) The backends.
	natives, err := t.thinBackends(ctx, j, work, modules)
	if err != nil {
		return err
	}
	// (3) The final link.
	fj := plain
	fj.Objects = make([]string, len(j.Objects))
	for i, o := range j.Objects {
		fj.Objects[i] = o
		if names[i] != "" {
			fj.Objects[i] = natives[names[i]]
		}
	}
	if j.Debug {
		err = t.pdbLink(ctx, fj)
	} else {
		var fargs []string
		if fargs, err = linkArgs(fj); err != nil {
			return err
		}
		_, err = t.runLinker(ctx, COFF, fargs, filepath.Dir(j.Output))
	}
	if j.LTOCacheDir != "" {
		pruneThinCache(j.LTOCacheDir, j.LTOCacheSize, time.Now())
	}
	return err
}

// thinIndexArgs is the command line of thinLink's index link, run in work:
// the inputs and options of the final link (plain, LTO cleared), the
// bitcode modules by their relative names (names[i] for object i, "" for
// a native one), never Debug (the index link writes no image, so no PDB),
// then the index-only options.
func thinIndexArgs(plain LinkJob, work string, names []string) ([]string, error) {
	ij := plain
	ij.Output = filepath.Join(work, "index.exe")
	ij.Debug = false
	ij.Objects = make([]string, len(plain.Objects))
	rel := map[string]string{}
	for i, o := range plain.Objects {
		ij.Objects[i] = o
		if i < len(names) && names[i] != "" {
			ij.Objects[i] = filepath.Join(work, names[i])
			rel[ij.Objects[i]] = names[i]
		}
	}
	iargs, err := linkArgs(ij)
	if err != nil {
		return nil, err
	}
	for k, a := range iargs {
		if r, ok := rel[a]; ok {
			iargs[k] = r
		}
	}
	return append(iargs, "/thinlto-index-only", "/thinlto-emit-imports-files"), nil
}

// LinkArgs is the linker command line of j after the executable, and
// ThinIndexArgs the command line of a COFF ThinLTO link's index link (j's
// options with LTO cleared, as thinLink passes them). Exported only for
// internal/codegen's TestLinkArgsDebug (a test-only import edge).
func LinkArgs(j LinkJob) ([]string, error) { return linkArgs(j) }

// ThinIndexArgs: see LinkArgs.
func ThinIndexArgs(j LinkJob, work string, names []string) ([]string, error) {
	plain := j
	plain.LTO = false
	return thinIndexArgs(plain, work, names)
}

// thinBackends compiles every module of work with its index, at most Jobs
// (else the CPU count) at a time, through the cache, and returns each
// module's native object by module name.
func (t *Tools) thinBackends(ctx context.Context, j LinkJob, work string, modules []string) (map[string]string, error) {
	jobs := j.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}
	if jobs > len(modules) {
		jobs = len(modules)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Memoize file reads across workers: ThinCacheKey reads each imported
	// bitcode module once per importer, so caching each path's bytes avoids
	// reading the same file from disk dozens of times.
	type cachedFile struct {
		once sync.Once
		data []byte
		err  error
	}
	var cacheMu sync.Mutex
	files := make(map[string]*cachedFile, len(modules)*3)
	read := func(name string) ([]byte, error) {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(work, path)
		}
		cacheMu.Lock()
		cf := files[path]
		if cf == nil {
			cf = &cachedFile{}
			files[path] = cf
		}
		cacheMu.Unlock()
		cf.once.Do(func() {
			cf.data, cf.err = os.ReadFile(path)
		})
		return cf.data, cf.err
	}

	// Schedule largest modules first (LPT): a hub module like main with many
	// ThinLTO imports can dominate wall time, so starting it at t=0 hides it
	// behind the rest of the build. Outputs stay indexed by i, so the final
	// link order is unchanged.
	order := make([]int, len(modules))
	weights := make([]int64, len(modules))
	for i, name := range modules {
		order[i] = i
		var w int64
		if fi, err := os.Stat(filepath.Join(work, name)); err == nil {
			w += fi.Size()
		}
		if fi, err := os.Stat(filepath.Join(work, name+".thinlto.bc")); err == nil {
			w += fi.Size() * 4
		}
		if fi, err := os.Stat(filepath.Join(work, name+".imports")); err == nil {
			w += fi.Size() * 256
		}
		weights[i] = w
	}
	sort.Slice(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if weights[ia] != weights[ib] {
			return weights[ia] > weights[ib]
		}
		return ia < ib
	})

	emptyCache := j.LTOCacheDir != "" && thinCacheEmpty(j.LTOCacheDir)
	natives := make([]string, len(modules))
	errs := make([]error, len(modules))
	var next atomic.Int64
	var wg sync.WaitGroup
	var workerErr atomic.Pointer[error]
	for range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ie := source.Guard("ThinLTO backend worker", source.Span{}, func() {
				for {
					k := int(next.Add(1) - 1)
					if k >= len(order) {
						return
					}
					i := order[k]
					name := modules[i]
					ie := source.Guard("ThinLTO backend "+name, source.Span{}, func() {
						natives[i], errs[i] = t.thinBackend(ctx, j, work, name, read, emptyCache)
					})
					if ie != nil {
						errs[i] = errors.New(ie.String())
					}
					if errs[i] != nil {
						cancel()
						return
					}
				}
			}); ie != nil {
				e := errors.New(ie.String())
				workerErr.CompareAndSwap(nil, &e)
				cancel()
			}
		}()
	}
	wg.Wait()
	// The first real failure, not a cancellation it caused.
	var first error
	for _, e := range errs {
		if e != nil && (first == nil || errors.Is(first, context.Canceled) && !errors.Is(e, context.Canceled)) {
			first = e
		}
	}
	if first == nil {
		if ep := workerErr.Load(); ep != nil {
			first = *ep
		}
	}
	if first != nil {
		return nil, first
	}
	byName := make(map[string]string, len(modules))
	for i, name := range modules {
		byName[name] = natives[i]
	}
	return byName, nil
}

// thinCacheTouchInterval avoids redundant Chtimes syscalls on rapid rebuilds
// while keeping LRU mtime updates well within thinCacheMaxAge (48h).
const thinCacheTouchInterval = 5 * time.Minute

// thinCacheEmpty reports whether dir currently contains no mkdt-*.o cache files.
func thinCacheEmpty(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, "mkdt-") && strings.HasSuffix(name, ".o") {
			return false
		}
	}
	return true
}

// thinBackend compiles one module of work with its index, through the
// cache, and returns its native object's path.
func (t *Tools) thinBackend(ctx context.Context, j LinkJob, work, name string, read func(string) ([]byte, error), emptyCache bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out := name + ".native.o"
	args := thinBackendArgs(name, name+".thinlto.bc", out, j.Tune)
	native := filepath.Join(work, out)
	cached := ""
	var keyDone chan struct{}
	var keyErr error
	if j.LTOCacheDir != "" {
		if emptyCache {
			// On a cold cache no lookup can hit; overlap ThinCacheKey I/O and
			// SHA-256 with clang so the largest module starts at t=0.
			keyDone = make(chan struct{})
			go func() {
				defer close(keyDone)
				if ie := source.Guard("ThinLTO cache key "+name, source.Span{}, func() {
					imports, err := read(name + ".imports")
					if err != nil {
						keyErr = err
						return
					}
					key, err := ThinCacheKey(t.Version.Line, args, name, name+".thinlto.bc", imports, read)
					if err != nil {
						keyErr = err
						return
					}
					cached = filepath.Join(j.LTOCacheDir, "mkdt-"+key+".o")
				}); ie != nil {
					keyErr = errors.New(ie.String())
				}
			}()
		} else {
			imports, err := read(name + ".imports")
			if err != nil {
				return "", err
			}
			key, err := ThinCacheKey(t.Version.Line, args, name, name+".thinlto.bc", imports, read)
			if err != nil {
				return "", err
			}
			cached = filepath.Join(j.LTOCacheDir, "mkdt-"+key+".o")
			if linkOrCopy(cached, native) == nil {
				now := time.Now()
				if fi, err := os.Stat(cached); err != nil || now.Sub(fi.ModTime()) > thinCacheTouchInterval {
					os.Chtimes(cached, now, now) // the prune keeps what is used
				}
				return native, nil
			}
		}
	}
	runArgs := t.thinCC1Args(ctx, j.Tune, name, name+".thinlto.bc", out, args)
	_, runErr := run(ctx, NameClang, t.Clang, runArgs, work)
	if keyDone != nil {
		<-keyDone
	}
	if runErr != nil {
		return "", runErr
	}
	if keyErr != nil {
		return "", keyErr
	}
	if cached != "" {
		storeThinCache(native, cached)
	}
	return native, nil
}

// pdbLink is a COFF link with Debug, made deterministic.
// lld-link's PDB records every input's path, the working directory and
// the command line, and /Brepro makes the PDB's GUID, which the image
// holds, a hash of the PDB. So the inputs are hard-linked (or copied) into
// a private directory under fixed names (o0000.o, a0000.lib, l0000.lib),
// lld-link runs there with relative names, /out and /pdb by base name and
// /pdbsourcepath:. in place of the working directory, and the image and
// its PDB are renamed beside j.Output. Two links of the same inputs from
// any two directories give the same image and the same PDB.
func (t *Tools) pdbLink(ctx context.Context, j LinkJob) error {
	work, err := os.MkdirTemp(filepath.Dir(j.Output), "mkpdb-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	sj := j
	sj.Output = filepath.Join(work, filepath.Base(j.Output))
	sj.Objects = make([]string, len(j.Objects))
	sj.Libs = make([]string, len(j.Libs))
	sj.Archives = make([]string, len(j.Archives))
	rel := map[string]string{}
	stage := func(src, name string) (string, error) {
		dst := filepath.Join(work, name)
		if err := linkOrCopy(src, dst); err != nil {
			return "", err
		}
		rel[dst] = name
		return dst, nil
	}
	for i, o := range j.Objects {
		if sj.Objects[i], err = stage(o, fmt.Sprintf("o%04d.o", i)); err != nil {
			return err
		}
	}
	for i, l := range j.Libs {
		if sj.Libs[i], err = stage(l, fmt.Sprintf("l%04d.lib", i)); err != nil {
			return err
		}
	}
	for i, a := range j.Archives {
		if sj.Archives[i], err = stage(a, StagedArchive(i)); err != nil {
			return err
		}
	}
	// A dynamic library's .def is an input, staged as the objects are,
	// and its import library an output written by base name and moved
	// beside j.ImpLib: the PDB records both on the command line.
	if j.Def != "" {
		if sj.Def, err = stage(j.Def, "exports.def"); err != nil {
			return err
		}
	}
	if j.ImpLib != "" {
		sj.ImpLib = filepath.Join(work, filepath.Base(j.ImpLib))
	}
	args, err := linkArgs(sj)
	if err != nil {
		return err
	}
	base := filepath.Base(j.Output)
	pdb := strings.TrimSuffix(base, filepath.Ext(base)) + ".pdb"
	for k, a := range args {
		if r, ok := rel[a]; ok {
			args[k] = r
		} else if a == "/out:"+sj.Output {
			args[k] = "/out:" + base
		} else if sj.Def != "" && a == "/def:"+sj.Def {
			args[k] = "/def:" + rel[sj.Def]
		} else if sj.ImpLib != "" && a == "/implib:"+sj.ImpLib {
			args[k] = "/implib:" + filepath.Base(sj.ImpLib)
		}
	}
	args = append(args, "/pdb:"+pdb, "/pdbsourcepath:.")
	if _, err := t.runLinkerIn(ctx, COFF, args, work, true); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(work, pdb), filepath.Join(filepath.Dir(j.Output), pdb)); err != nil {
		return err
	}
	if j.ImpLib != "" {
		if err := os.Rename(sj.ImpLib, j.ImpLib); err != nil {
			return err
		}
	}
	return os.Rename(sj.Output, j.Output)
}

// loadConfigSize is the Size of loadConfigObject's
// IMAGE_LOAD_CONFIG_DIRECTORY64: 0x140, the size the Windows 10 SDK's
// load configuration has, which covers DependentLoadFlags (offset 0x4E).
const loadConfigSize = 0x140

// loadConfigObject is a COFF object holding one 8-aligned read-only
// section with _load_config_used: an IMAGE_LOAD_CONFIG_DIRECTORY64 whose
// Size is loadConfigSize and every other field zero, which lld-link fills
// with /dependentloadflag. A CRT's loadcfg object supplies it in a
// C program; an Mk program links no CRT. Zero fields mean what an absent
// field means (no security cookie, no CFG, no SEH table, which x64 does
// not use). The bytes are fixed, so a link stays deterministic.
func loadConfigObject() []byte {
	const name = "_load_config_used"
	const dataAt = 20 + 40
	symAt := dataAt + loadConfigSize
	var b []byte
	u16 := func(v uint16) { b = binary.LittleEndian.AppendUint16(b, v) }
	u32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	// File header: x64, one section, the symbol table after the data.
	u16(0x8664)
	u16(1)
	u32(0)
	u32(uint32(symAt))
	u32(1)
	u16(0)
	u16(0)
	// Section header: .rdata, initialized data, 8-byte aligned, read.
	b = append(b, ".rdata"...)
	b = append(b, 0, 0) // the name, padded to 8 bytes
	u32(0)
	u32(0)
	u32(loadConfigSize)
	u32(dataAt)
	u32(0)
	u32(0)
	u16(0)
	u16(0)
	u32(0x00000040 | 0x00400000 | 0x40000000)
	// The directory: Size, then zeros.
	u32(loadConfigSize)
	b = append(b, make([]byte, loadConfigSize-4)...)
	// The symbol: its name in the string table at offset 4, value 0,
	// section 1, no type, external.
	u32(0)
	u32(4)
	u32(0)
	u16(1)
	u16(0)
	b = append(b, 2, 0)
	u32(uint32(4 + len(name) + 1))
	b = append(b, name...)
	return append(b, 0)
}

// LinkStub links the stub shared object out from obj, an object defining
// each name of a library's list: ld.lld -shared with soname,
// so the executable's DT_NEEDED is the loader's exact name whatever out
// is called, and the version script verScript ("" for none), so a
// name@VERSION of the list is defined at that version and the
// executable's reference to it is versioned as a link against the real
// library would version it. The stub holds no code anyone runs.
func (t *Tools) LinkStub(ctx context.Context, obj, soname, verScript, out string) error {
	args, err := stubArgs(obj, soname, verScript, out)
	if err != nil {
		return err
	}
	_, err = t.runLinker(ctx, ELF, args, filepath.Dir(out))
	return err
}

// stubArgs is LinkStub's command line after ld.lld.
func stubArgs(obj, soname, verScript, out string) ([]string, error) {
	for _, p := range []string{obj, out} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("toolchain: stub paths must be absolute: %q", p)
		}
	}
	if soname == "" || strings.ContainsAny(soname, "/\\") {
		return nil, fmt.Errorf("toolchain: bad soname %q", soname)
	}
	args := []string{"-shared", "-soname=" + soname, "--build-id=none", "--hash-style=gnu"}
	if verScript != "" {
		if !filepath.IsAbs(verScript) {
			return nil, fmt.Errorf("toolchain: stub paths must be absolute: %q", verScript)
		}
		args = append(args, "--version-script="+verScript)
	}
	return append(args, "-o", out, obj), nil
}

// StagedArchive is the fixed name pdbLink gives the i-th archive of a
// --debug link, which lld then reports; the driver maps it back to its
// link.
func StagedArchive(i int) string { return fmt.Sprintf("a%04d.lib", i) }

// linkOrCopy makes dst a hard link to src, or a copy where a link is not
// possible (another volume).
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

var thinTmpSeq atomic.Uint64

// storeThinCache puts a backend's object into the cache through a
// temporary file and a rename, so a reader never sees half an object. A
// failure only loses the entry.
func storeThinCache(obj, cached string) {
	dir := filepath.Dir(cached)
	// Fast path: hard-link obj directly to a temp name in the cache dir and
	// rename into place, avoiding a full read+write copy when on the same volume.
	// Prefix with mkdt-tmp- so pruneThinCache cleans up after a crashed writer.
	tmpLink := filepath.Join(dir, fmt.Sprintf("mkdt-tmp-%d-%d", os.Getpid(), thinTmpSeq.Add(1)))
	if os.Link(obj, tmpLink) == nil {
		if os.Rename(tmpLink, cached) == nil {
			return
		}
		os.Remove(tmpLink)
	}
	b, err := os.ReadFile(obj)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "mkdt-tmp-")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), cached) != nil {
		os.Remove(tmp.Name())
	}
}

// thinCacheMaxAge is the age past which a backend cache entry is pruned,
// the prune_after of the policy given to lld.
const thinCacheMaxAge = 48 * time.Hour

// pruneThinCache removes the mkdt- entries of dir older than
// thinCacheMaxAge, then the oldest until the rest fit in limit bytes. lld's
// own entries (llvmcache-) are lld's to prune.
func pruneThinCache(dir string, limit int64, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var live []entry
	var total int64
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "mkdt-") || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, name)
		if strings.HasPrefix(name, "mkdt-tmp-") {
			if now.Sub(info.ModTime()) > time.Hour { // a writer that died
				os.Remove(p)
			}
			continue
		}
		if now.Sub(info.ModTime()) > thinCacheMaxAge {
			os.Remove(p)
			continue
		}
		live = append(live, entry{p, info.Size(), info.ModTime()})
		total += info.Size()
	}
	if limit <= 0 || total <= limit {
		return
	}
	sort.Slice(live, func(a, b int) bool {
		if !live[a].mod.Equal(live[b].mod) {
			return live[a].mod.Before(live[b].mod)
		}
		return live[a].path < live[b].path
	})
	for _, e := range live {
		if total <= limit {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}
