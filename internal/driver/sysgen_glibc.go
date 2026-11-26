// Why: POSIX declarations and link versions must agree with the target's
// glibc ABI even when the system view is generated on another host.
// What: readGlibcInput opens the Debian multiarch headers and libc ELF.
// How: only default public function exports available at the minimum
// supported glibc release qualify for an automatic link entry.

package driver

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const glibcSONAME = "libc.so.6"
const glibcABI = "2.34"

type glibcInput struct {
	dirs    []string
	exports map[string]string
	version string
	uapi    string
	libHash string
}

func readGlibcInput(target, root string) (*glibcInput, error) {
	machine := map[string]elf.Machine{"x86_64-linux": elf.EM_X86_64, "aarch64-linux": elf.EM_AARCH64}[target]
	multiarch := target + "-gnu"
	include := filepath.Join(root, "usr", "include")
	headerVersion, err := sysHeaderVersion(filepath.Join(include, "features.h"), "__GLIBC__", "__GLIBC_MINOR__")
	if err != nil {
		return nil, err
	}
	uapi, err := sysHeaderVersion(filepath.Join(include, "linux", "version.h"), "LINUX_VERSION_MAJOR", "LINUX_VERSION_PATCHLEVEL", "LINUX_VERSION_SUBLEVEL")
	if err != nil {
		return nil, err
	}
	lib := filepath.Join(root, "lib", multiarch, glibcSONAME)
	if !isRegular(lib) {
		lib = filepath.Join(root, "usr", "lib", multiarch, glibcSONAME)
	}
	blob, err := os.ReadFile(lib)
	if err != nil {
		return nil, err
	}
	f, err := elf.NewFile(bytes.NewReader(blob))
	if err != nil {
		return nil, fmt.Errorf("sysview: %s: %w", lib, err)
	}
	soname, err := f.DynString(elf.DT_SONAME)
	if err != nil {
		return nil, fmt.Errorf("sysview: %s: %w", lib, err)
	}
	if f.Machine != machine || f.Type != elf.ET_DYN || f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || !slices.Equal(soname, []string{glibcSONAME}) {
		return nil, fmt.Errorf("sysview: %s is not %s for %s", lib, glibcSONAME, target)
	}
	syms, err := f.DynamicSymbols()
	if err != nil {
		return nil, fmt.Errorf("sysview: %s: %w", lib, err)
	}
	versions := map[string]string{}
	for _, s := range syms {
		if s.Section == elf.SHN_UNDEF || !s.HasVersion || s.VersionIndex.IsHidden() || !supportedGlibcExport(s.Version) {
			continue
		}
		bind, typ, vis := elf.ST_BIND(s.Info), elf.ST_TYPE(s.Info), elf.ST_VISIBILITY(s.Other)
		if (bind == elf.STB_GLOBAL || bind == elf.STB_WEAK) && (typ == elf.STT_FUNC || typ == elf.STT_GNU_IFUNC) && (vis == elf.STV_DEFAULT || vis == elf.STV_PROTECTED) {
			versions[s.Name] = s.Version
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("sysview: %s has no default GLIBC function exports at or before %s", lib, glibcABI)
	}
	return &glibcInput{dirs: []string{filepath.Join(include, multiarch), include}, exports: versions,
		version: headerVersion, uapi: uapi, libHash: fmt.Sprintf("%x", sha256.Sum256(blob))}, nil
}

func supportedGlibcExport(name string) bool {
	v, ok := strings.CutPrefix(name, "GLIBC_")
	if !ok {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || strconv.Itoa(n) != p || n < 0 {
			return false
		}
		nums[i] = n
	}
	return slices.Compare(nums[:], []int{2, 34, 0}) <= 0
}

func sysHeaderVersion(path string, names ...string) (string, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(names))
	for line := range strings.SplitSeq(string(text), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "#define" {
			continue
		}
		if i := slices.Index(names, fields[1]); i >= 0 {
			if _, err := strconv.ParseUint(fields[2], 10, 32); err == nil {
				parts[i] = fields[2]
			}
		}
	}
	if slices.Contains(parts, "") {
		return "", fmt.Errorf("sysview: %s does not define its version (%s)", path, strings.Join(names, ", "))
	}
	return strings.Join(parts, "."), nil
}
