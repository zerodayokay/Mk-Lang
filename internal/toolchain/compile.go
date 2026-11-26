// Why: every Mk object is clang's compile of one pack's LLVM IR; the
// command line decides what the object holds, so it must be exact and
// reproducible across directories and machines.
//
// What: CompileJob, OptLevel, Compile, compileArgs and CompileKey (the part
// of a job's tuning that changes the object).
//
// How: clang only ever compiles IR here, never C. The flags pin everything
// that would otherwise leak into the object (timestamps, absolute paths,
// the cc1 command line), and the optimization level and tuning are part of
// the object's cache key.

package toolchain

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// OptLevel is the clang optimization level: O0 for dev and test, O3 for
// release, Oz for the size profile (mk build --profile size). The level is
// part of every object and pack key (the driver's objKey and prekey hash it),
// so a size build never shares an object with a release one.
type OptLevel uint8

// The levels the profiles use.
const (
	O0 OptLevel = iota
	O3
	Oz
)

func (o OptLevel) flag() string {
	switch o {
	case O3:
		return "-O3"
	case Oz:
		return "-Oz"
	}
	return "-O0"
}

// CompileJob is one `.ll` to one object. Input and Output are absolute
// paths; the output's directory is the tool's working directory.
type CompileJob struct {
	Triple string
	Input  string
	Output string
	Opt    OptLevel
	LTO    bool   // -flto=thin: the object is bitcode
	CPU    string // "" means the target baseline the driver chose; never "native"
	// Tune acts on the compile only with LTO. With Tune.FatLTO the bitcode is
	// full LTO's (-flto=full), and since clang's full-LTO pre-link pipeline
	// runs the loop vectorizer, the SLP vectorizer and both unrollers, the
	// compile takes the LTO steps' LLVM options, all but the loop alignment,
	// which is codegen's, at the link (Tuning.backendOptions); with no unroll
	// option that includes ltoUnroll, 400, where a ThinLTO pack compile has
	// LLVM's 300. Otherwise only an unroll=<N> threshold reaches the compile
	// (Tuning.prelinkOptions): ThinLTO's pre-link pipeline runs the full
	// unroller (loop-unroll-full) and no vectorizer, so -unroll-threshold is
	// the one option of an experiment that changes its bitcode. With the zero
	// Tuning, or unroll=llvm, the compile has no LLVM option.
	Tune Tuning
	// DirectDriver skips -cc1 template probing (used for one-off compiles like
	// the single support pack where probing clang -### costs more than running
	// the driver once).
	DirectDriver bool
}

// fullLTO reports whether j compiles to full-LTO bitcode (fatlto).
func (j CompileJob) fullLTO() bool { return j.LTO && j.Tune.FatLTO }

// Compile runs clang on LLVM IR, never on C. The error is
// *ToolError, or a job error (relative path, CPU "native") before any
// process starts.
func (t *Tools) Compile(ctx context.Context, j CompileJob) error {
	args, err := compileArgs(j)
	if err != nil {
		return err
	}
	runArgs := args
	if !j.DirectDriver {
		runArgs = t.compileCC1Args(ctx, j, args)
	}
	_, err = run(ctx, NameClang, t.Clang, runArgs, filepath.Dir(j.Output))
	return err
}

// compileArgs is the clang command line after the executable:
// --target=T -x ir -c IN -o OUT -O0|-O3|-Oz [-flto=thin|full] [-march=CPU|-mcpu=CPU]
// -ffunction-sections -fdata-sections -fno-addrsig -fdebug-compilation-dir=.
// [-mno-outline-atomics] [-mno-incremental-linker-compatible -Xclang
// -gno-codeview-command-line]. -mno-outline-atomics is for AArch64 triples
// (NoOutlineAtomics).
// -march names the CPU on x86_64 and -mcpu on AArch64. The last two are for
// COFF triples only: without them clang writes a timestamp into the COFF
// header and the cc1 command line, with the input's absolute path and the
// working directory, into the CodeView LF_BUILDINFO record, so a dev object
// would differ between two directories. The driver flag -gno-codeview-command-line
// is ignored when no -g is given (the line tables come from the IR), so it
// goes to cc1 through -Xclang. Measured on 2026-11-04 with clang 21.1.8: one
// .ll with a CodeView line table compiled at -O0 in two directories gave
// different objects without the flags and byte-identical ones with them
// (TestCOFFObjectsReproducible).
func compileArgs(j CompileJob) ([]string, error) {
	if j.Triple == "" {
		return nil, errors.New("toolchain: compile job without a triple")
	}
	if !filepath.IsAbs(j.Input) || !filepath.IsAbs(j.Output) {
		return nil, fmt.Errorf("toolchain: compile paths must be absolute: %q, %q", j.Input, j.Output)
	}
	if j.CPU == "native" {
		return nil, errors.New("toolchain: CPU \"native\" is never used (it would differ by host)")
	}
	args := []string{ClangTargetArg(j.Triple), "-x", "ir", "-c", j.Input, "-o", j.Output, j.Opt.flag()}
	if j.fullLTO() {
		args = append(args, "-flto=full")
	} else if j.LTO {
		args = append(args, "-flto=thin")
	}
	if j.CPU != "" {
		if ArchOf(j.Triple) == "arm64" {
			args = append(args, "-mcpu="+j.CPU)
		} else {
			args = append(args, "-march="+j.CPU)
		}
	}
	args = append(args, "-ffunction-sections", "-fdata-sections", "-fno-addrsig", "-fdebug-compilation-dir=.")
	args = append(args, NoOutlineAtomics(j.Triple)...)
	if FormatOf(j.Triple) == COFF {
		args = append(args, "-mno-incremental-linker-compatible", "-Xclang", "-gno-codeview-command-line")
	}
	for _, o := range j.llvmOptions() {
		args = append(args, "-mllvm", o)
	}
	return args, nil
}

// NoOutlineAtomics is -mno-outline-atomics for an AArch64 triple, nothing
// otherwise. clang's driver turns outline atomics on for aarch64 Linux when
// it finds a GCC installation of 9.3.1 or later on the build host, and an
// atomic then becomes a call of a libgcc or compiler-rt helper
// (__aarch64_cas4_acq_rel and its kin), which mk does not link: so a build
// would link on one host and not on another. With the flag every atomic
// is inline LL/SC code (the AArch64 baseline has no LSE), on every host.
func NoOutlineAtomics(triple string) []string {
	if ArchOf(triple) == "arm64" {
		return []string{"-mno-outline-atomics"}
	}
	return nil
}

// llvmOptions are the LLVM options j's Tune gives the compile, without
// -mllvm: under fatlto the backend options (the loop alignment is
// codegen's, at the link), else, with LTO, the pre-link ones.
func (j CompileJob) llvmOptions() []string {
	switch {
	case j.fullLTO():
		return j.Tune.backendOptions()
	case j.LTO:
		return j.Tune.prelinkOptions()
	}
	return nil
}

// CompileKey is what of j's Tune changes the object: "" when no
// LLVM option of the Tune reaches the compile (llvmOptions), as for every
// default build, novec or the support pack; else the route and the
// options, which the driver hashes into the object and pack keys.
func (j CompileJob) CompileKey() string {
	opts := j.llvmOptions()
	switch {
	case j.fullLTO():
		return "flto=full " + j.Tune.String()
	case len(opts) > 0:
		return "flto=thin " + strings.Join(opts, " ")
	}
	return ""
}
