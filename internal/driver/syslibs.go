// Why: a C binding on Windows may declare a function of a system DLL that
// std's .def lists do not name, and the link needs an import library for it.
//
// What: systemDLLs, systemExports, peExports (a PE export-table reader),
// cObjectUndefined, addSystemLibs and shimObjects.
//
// How: a name a C binding declares, or a C import's shim calls (never a C
// source: those link against std.libc only), is looked up in the export
// tables of the DLLs Visual Studio links by default plus Winsock, in that
// order, and the DLL that exports it gets an import library with the names
// used. The tables are read from the host's System32 with debug/pe, so this
// works only on a Windows host; elsewhere such a name stays undefined and
// the link reports it.

package driver

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// systemDLLs is the lookup order: Visual Studio's default import libraries
// for a Windows desktop project, then Winsock. A name two DLLs export is
// taken from the first.
var systemDLLs = []string{"kernel32", "user32", "gdi32", "winspool", "comdlg32", "advapi32", "shell32", "ole32", "oleaut32", "ws2_32"}

var (
	sysExportsOnce sync.Once
	sysExports     map[string]string // exported name to its DLL's base name
)

// systemExports is the export map of systemDLLs from the host's System32,
// read once per process; empty when the host is not Windows.
func systemExports() map[string]string {
	if testNoSystem32 { // links.go: as on a host that is not Windows
		return map[string]string{}
	}
	sysExportsOnce.Do(func() {
		sysExports = map[string]string{}
		if runtime.GOOS != "windows" {
			return
		}
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		for _, dll := range systemDLLs {
			names, err := peExports(filepath.Join(root, "System32", dll+".dll"))
			if err != nil {
				continue
			}
			for _, n := range names {
				if _, ok := sysExports[n]; !ok {
					sysExports[n] = dll
				}
			}
		}
	})
	return sysExports
}

// peExports lists the names in a PE file's export directory.
func peExports(path string) ([]string, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var dir pe.DataDirectory
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if oh.NumberOfRvaAndSizes == 0 {
			return nil, errors.New("no export directory")
		}
		dir = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
	case *pe.OptionalHeader32:
		if oh.NumberOfRvaAndSizes == 0 {
			return nil, errors.New("no export directory")
		}
		dir = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
	default:
		return nil, errors.New("no optional header")
	}
	if dir.VirtualAddress == 0 {
		return nil, errors.New("no export directory")
	}
	// at is the bytes from an RVA to the end of the section that holds
	// it; each section is read once.
	data := map[*pe.Section][]byte{}
	at := func(rva uint32) []byte {
		for _, s := range f.Sections {
			if rva >= s.VirtualAddress && rva < s.VirtualAddress+s.VirtualSize {
				d, ok := data[s]
				if !ok {
					d, _ = s.Data()
					data[s] = d
				}
				if off := rva - s.VirtualAddress; off < uint32(len(d)) {
					return d[off:]
				}
				return nil
			}
		}
		return nil
	}
	ed := at(dir.VirtualAddress) // IMAGE_EXPORT_DIRECTORY
	if len(ed) < 40 {
		return nil, errors.New("export directory outside the sections")
	}
	count := binary.LittleEndian.Uint32(ed[24:])
	ptrs := at(binary.LittleEndian.Uint32(ed[32:]))
	if count > 1<<16 || uint64(len(ptrs)) < 4*uint64(count) {
		return nil, errors.New("export names outside the sections")
	}
	out := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		name := at(binary.LittleEndian.Uint32(ptrs[4*i:]))
		if j := bytes.IndexByte(name, 0); j > 0 {
			out = append(out, string(name[:j]))
		}
	}
	return out, nil
}

// cObjectUndefined lists the undefined external symbols of COFF objects,
// with a dllimport reference's __imp_ prefix removed.
func cObjectUndefined(objs []string) []string {
	var out []string
	for _, p := range objs {
		f, err := pe.Open(p)
		if err != nil {
			continue
		}
		for _, s := range f.Symbols {
			if s.SectionNumber == 0 && s.StorageClass == 2 /* IMAGE_SYM_CLASS_EXTERNAL */ && s.Value == 0 {
				out = append(out, strings.TrimPrefix(s.Name, "__imp_"))
			}
		}
		f.Close()
	}
	return out
}

// addSystemLibs extends libs (std's, by DLL) with the system DLL imports
// of the names in used that no lib names: a std DLL's list gains them, any
// other DLL gets a lib of its own, inserted in DLL name order.
func addSystemLibs(libs []importLib, used []string) []importLib {
	have := map[string]bool{}
	for _, l := range libs {
		for _, n := range l.names {
			have[n] = true
		}
	}
	var exp map[string]string
	extra := map[string][]string{}
	for _, n := range used {
		if have[n] {
			continue
		}
		if exp == nil {
			exp = systemExports()
		}
		if dll, ok := exp[n]; ok {
			have[n] = true
			extra[dll] = append(extra[dll], n)
		}
	}
	if len(extra) == 0 {
		return libs
	}
	byDLL := make(map[string][]string, len(extra))
	for dll, names := range extra {
		byDLL[dll+".dll"] = names
	}
	return mergeImportLibs(libs, byDLL)
}

// addSysViewLibs extends libs with eager link entries from the program's
// system views for any symbol in used not already supplied.
func addSysViewLibs(libs []importLib, used []string, sv *sysViews) []importLib {
	if sv == nil || len(sv.links) == 0 {
		return libs
	}
	have := map[string]bool{}
	for _, l := range libs {
		for _, n := range l.names {
			have[n] = true
		}
	}
	extra := map[string][]string{}
	for _, n := range used {
		if have[n] {
			continue
		}
		if lk := sv.links[n]; lk.Lib != "" && !lk.Lazy {
			have[n] = true
			dll := strings.ToLower(lk.Lib)
			extra[dll] = append(extra[dll], n)
		}
	}
	return mergeImportLibs(libs, extra)
}

// mergeImportLibs merges extra (lowercased DLL filename -> symbols) into
// libs, reusing any matching existing importLib by fileName and adding new
// entries sorted by base DLL name.
func mergeImportLibs(libs []importLib, extra map[string][]string) []importLib {
	if len(extra) == 0 {
		return libs
	}
	for i := range libs {
		key := strings.ToLower(libs[i].fileName())
		if more := extra[key]; len(more) > 0 {
			names := append(append([]string(nil), libs[i].names...), more...)
			slices.Sort(names)
			libs[i].names = slices.Compact(names)
			delete(extra, key)
		}
	}
	for dll, names := range extra {
		slices.Sort(names)
		names = slices.Compact(names)
		base := strings.TrimSuffix(dll, ".dll")
		file := ""
		if base == dll {
			file = dll
		}
		libs = append(libs, importLib{dll: base, file: file, names: names})
	}
	slices.SortStableFunc(libs, func(a, b importLib) int { return strings.Compare(a.dll, b.dll) })
	return libs
}

// shimObjects are the C import shims among a link's C objects (cObjectsOf
// names each mk_shim.o).
func shimObjects(objs []string) []string {
	var out []string
	for _, o := range objs {
		if filepath.Base(o) == "mk_shim.o" {
			out = append(out, o)
		}
	}
	return out
}
