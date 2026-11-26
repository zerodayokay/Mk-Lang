// Why: a package's own C and assembly sources (c_sources, asm_sources) and
// the generated C import shim must be compiled for the target by clang;
// this is the one place clang compiles C.
//
// What: CJob, CompileC, CompileAsm and their command line cArgs (CArgs
// for tests): one object per source file, linked against std.libc.
//
// How: never a C runtime and never the host's headers: the job uses gnu11,
// as the C import reads the headers, and -ffreestanding except for the
// hosted shim. With DepFile set, clang writes the -MD list of every file the
// job read, so the object's cache key covers their contents.

package toolchain

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// CJob is one C or assembly source to one object. Every path is absolute;
// the output's directory is the tool's working directory.
type CJob struct {
	Triple string
	Input  string // a .c file, or a .s or .S file for CompileAsm
	Output string
	Opt    OptLevel
	// ResourceDir is clang's resource directory (ResourceDir): its include
	// directory is the only system header directory unless Include names
	// more, since the job runs with -nostdinc.
	ResourceDir string
	// Include are -I directories (the package root, the shim's directory),
	// then System are -isystem directories (a sysroot's), in order.
	Include, System []string
	Sysroot         string   // --sysroot; "" for none
	Defines         []string // NAME or NAME=VALUE, as -D
	Flags           []string // the manifest's flags, passed after every flag above
	// DepFile, when set, is where clang writes the -MD list of every file
	// the job read (the C object's cache key covers their contents).
	DepFile string
	// Hosted compiles as the C import reads the headers: without
	// -ffreestanding, so __STDC_HOSTED__ and the system headers' choices
	// are the binding's. The shim is hosted; C sources are not.
	Hosted bool
}

// CompileC compiles one C file for the target with no C runtime in reach:
// -nostdinc (only the resource directory's and the given headers),
// -ffreestanding and -fno-builtin (no call is assumed to be libc's unless
// the source makes it), -fno-stack-protector, and on Windows
// -mno-stack-arg-probe (no __chkstk: a Mk program links no CRT). The error
// is *ToolError, or a job error before any process starts.
func (t *Tools) CompileC(ctx context.Context, j CJob) error {
	args, err := cArgs(j, false)
	if err != nil {
		return err
	}
	_, err = run(ctx, NameClang, t.Clang, args, filepath.Dir(j.Output))
	return err
}

// CompileAsm assembles one .s file (or preprocesses and assembles a .S
// file) for the target with clang's integrated assembler.
func (t *Tools) CompileAsm(ctx context.Context, j CJob) error {
	args, err := cArgs(j, true)
	if err != nil {
		return err
	}
	_, err = run(ctx, NameClang, t.Clang, args, filepath.Dir(j.Output))
	return err
}

// cArgs is the clang command line after the executable. The order is
// fixed, so an object's cache key can be the command line itself.
func cArgs(j CJob, asm bool) ([]string, error) {
	if j.Triple == "" {
		return nil, errors.New("toolchain: C job without a triple")
	}
	for _, p := range append([]string{j.Input, j.Output}, append(append([]string{}, j.Include...), j.System...)...) {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("toolchain: C job paths must be absolute: %q", p)
		}
	}
	args := []string{ClangTargetArg(j.Triple)}
	if asm {
		lang := "assembler"
		if strings.HasSuffix(j.Input, ".S") {
			lang = "assembler-with-cpp"
		}
		args = append(args, "-x", lang)
	} else {
		// gnu11, as internal/cimport reads the headers (baseArgs): c11 would
		// define __STRICT_ANSI__ and refuse GNU forms the binding accepted, so
		// a header's static inline the binding names could fail in the shim,
		// or a header could declare otherwise for a C source.
		args = append(args, "-x", "c", "-std=gnu11")
		if !j.Hosted {
			args = append(args, "-ffreestanding")
		}
		args = append(args, "-fno-builtin", "-fno-stack-protector", "-fno-common", "-ffunction-sections", "-fdata-sections")
		if OSOf(j.Triple) == "windows" {
			args = append(args, "-mno-stack-arg-probe")
		}
		args = append(args, NoOutlineAtomics(j.Triple)...)
	}
	args = append(args, "-nostdinc")
	if j.ResourceDir != "" {
		args = append(args, "-isystem", filepath.Join(j.ResourceDir, "include"))
	}
	if j.Sysroot != "" {
		args = append(args, "--sysroot="+j.Sysroot)
	}
	for _, d := range j.Include {
		args = append(args, "-I", d)
	}
	for _, d := range j.System {
		args = append(args, "-isystem", d)
	}
	for _, d := range j.Defines {
		args = append(args, "-D", d)
	}
	args = append(args, "-c", j.Input, "-o", j.Output, j.Opt.flag(), "-fdebug-compilation-dir=.")
	if j.DepFile != "" {
		args = append(args, "-MD", "-MF", j.DepFile, "-MT", "obj")
	}
	if FormatOf(j.Triple) == COFF {
		args = append(args, "-mno-incremental-linker-compatible")
	}
	args = append(args, j.Flags...)
	return args, nil
}

// CArgs is the command line CompileC (asm false) or CompileAsm (asm true)
// runs, for cache keys and logs.
func CArgs(j CJob, asm bool) ([]string, error) { return cArgs(j, asm) }
