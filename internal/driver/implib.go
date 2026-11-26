// Why: automatic linking for system views (at generation time) and for
// packages with `pkg.CImport(..., sdk: true)` (at link time) needs to map C
// function symbols to their Windows DLLs from the SDK's import libraries
// without shelling out to llvm-readobj or using cgo.
//
// What: implibEntry, implibIndex, readImplib, readImplibDir, windowsSDKLibDir,
// sdkLibDirs, classicDLLs, isClassicDLL, and implibTieBreak.
//
// How: readImplib walks COFF ar archives (`!<arch>\n`) in pure Go, parses
// short import headers (`IMPORT_OBJECT_HEADER`: Sig1=0, Sig2=0xFFFF,
// Version=0), discards any library naming more than three distinct DLLs
// (umbrella and API-set libraries), keeps only code imports with plain C
// export names, and records lowercased DLL names per symbol. Ambiguities
// settle in a fixed, documented order: classic desktop DLLs first, then
// implibTieBreak pairs; anything still multi-DLL is returned as ambiguous.

package driver

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// classicDLLs is the Windows classic desktop DLL precedence order:
// Visual Studio's default import libraries plus Winsock. Any library
// outside this set binds lazily in system views.
var classicDLLs = []string{
	"kernel32.dll",
	"user32.dll",
	"gdi32.dll",
	"advapi32.dll",
	"shell32.dll",
	"ole32.dll",
	"oleaut32.dll",
	"ws2_32.dll",
	"winspool.drv",
	"comdlg32.dll",
}

// isClassicDLL reports whether dll (case-insensitive) is in classicDLLs.
func isClassicDLL(dll string) bool {
	low := strings.ToLower(dll)
	for _, c := range classicDLLs {
		if low == c {
			return true
		}
	}
	return false
}

// implibTieBreak settles multi-DLL symbols not covered by classicDLLs:
// whenever both winner and loser appear in a symbol's candidate DLLs,
// loser is eliminated.
var implibTieBreak = []struct{ winner, loser string }{
	// CNG primitives in windows.h: bcrypt.dll is the primitive library;
	// ncrypt.dll only re-exports them for compatibility.
	{"bcrypt.dll", "ncrypt.dll"},
	// AcceptEx, GetAcceptExSockaddrs, TransmitFile, WSARecvEx: wsock32.dll
	// is legacy Winsock 1.1 forwarding to mswsock.dll.
	{"mswsock.dll", "wsock32.dll"},
	// RpcIfIdVectorFree: rpcrt4.dll is the core RPC runtime; rpcns4.dll is
	// the obsolete Name Service library.
	{"rpcrt4.dll", "rpcns4.dll"},
	// RtlGetSystemGlobalData: vertdll.dll is the VBS enclave runtime, not
	// desktop Windows.
	{"ntdll.dll", "vertdll.dll"},
	// WaitOnAddress, WakeByAddressAll, WakeByAddressSingle:
	// synchronization.lib targets the API-set contract over enclave vertdll.dll.
	{"api-ms-win-core-synch-l1-2-0.dll", "vertdll.dll"},
}

// implibEntry is one (symbol, DLL) import extracted from a COFF .lib.
type implibEntry struct {
	Symbol string
	DLL    string
}

// implibIndex maps each exported C function symbol to the sorted, unique
// lowercased DLL names that export it across non-umbrella import libraries.
type implibIndex struct {
	bySym map[string][]string
}

// readImplib parses a COFF import library (.lib) in memory and returns its
// code export pairs. Libraries naming more than 3 distinct DLLs (umbrella
// and API-set libraries) return nil, nil. Non-short-import members (static
// objects such as dxguid.lib), data/const imports, and non-plain-C names
// are ignored.
//
// Note: COFF IMPORT_OBJECT_HEADER NameType=IMPORT_ORDINAL (0) is kept when
// it carries a plain C symbol name: in Windows SDK import libraries (such as
// d3d12.lib for D3D12CreateDevice and D3D12GetDebugInterface, ws2_32.lib,
// oleaut32.lib, winspool.lib), named exports with fixed ordinals carry
// NameType=0 while still exporting by name in their DLLs.
func readImplib(b []byte) ([]implibEntry, error) {
	const arMagic = "!<arch>\n"
	if !bytes.HasPrefix(b, []byte(arMagic)) {
		return nil, errors.New("not a COFF archive")
	}
	var longNames []byte
	dlls := map[string]bool{}
	var out []implibEntry
	for off := len(arMagic); off < len(b); {
		if off+60 > len(b) {
			return nil, errors.New("bad archive member header")
		}
		hdr := b[off : off+60]
		if hdr[58] != '`' || hdr[59] != '\n' {
			return nil, errors.New("bad archive member header")
		}
		size, err := strconv.ParseUint(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || size > uint64(len(b)-(off+60)) {
			return nil, errors.New("bad archive member header")
		}
		name := strings.TrimRight(string(hdr[0:16]), " ")
		data := b[off+60 : off+60+int(size)]
		off += 60 + int(size)
		if off%2 == 1 {
			off++
		}
		switch {
		case name == "/" || name == "/SYM64/" || name == "__.SYMDEF" || name == "__.SYMDEF SORTED":
			continue
		case name == "//":
			longNames = data
			continue
		case strings.HasPrefix(name, "#1/"):
			n, err := strconv.Atoi(name[3:])
			if err != nil || n < 0 || n > len(data) {
				return nil, errors.New("bad BSD member name")
			}
			data = data[n:]
		case strings.HasPrefix(name, "/"):
			n, err := strconv.Atoi(name[1:])
			if err != nil || n < 0 || n >= len(longNames) {
				return nil, errors.New("bad long member name")
			}
		}
		if len(data) < 20 || binary.LittleEndian.Uint16(data[0:]) != 0 ||
			binary.LittleEndian.Uint16(data[2:]) != 0xFFFF || binary.LittleEndian.Uint16(data[4:]) != 0 {
			continue
		}
		flags := binary.LittleEndian.Uint16(data[18:])
		impType := flags & 0x3 // 0=IMPORT_CODE, 1=IMPORT_DATA, 2=IMPORT_CONST
		rest := data[20:]
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			continue
		}
		sym := string(rest[:i])
		rest = rest[i+1:]
		j := bytes.IndexByte(rest, 0)
		if j <= 0 {
			continue
		}
		dll := strings.ToLower(string(rest[:j]))
		if !dlls[dll] {
			dlls[dll] = true
			if len(dlls) > 3 {
				return nil, nil // umbrella or API-set library
			}
		}
		if impType != 0 || !validImplibExport(sym) {
			continue
		}
		out = append(out, implibEntry{Symbol: sym, DLL: dll})
	}
	return out, nil
}

// validImplibExport reports whether n is a plain C export identifier that
// toolchain.ImportLib (DefText) and lazy stubs accept.
func validImplibExport(n string) bool {
	if n == "" || (n[0] >= '0' && n[0] <= '9') {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	switch strings.ToUpper(n) {
	case "BASE", "CONSTANT", "DATA", "EXPORTAS", "EXPORTS", "HEAPSIZE",
		"LIBRARY", "NAME", "NONAME", "PRIVATE", "STACKSIZE", "VERSION":
		return false
	}
	return true
}

// readImplibDir reads every *.lib in dir (in filename order) and builds an
// implibIndex. Unreadable or non-archive files are skipped.
func readImplibDir(dir string) (*implibIndex, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	idx := &implibIndex{bySym: map[string][]string{}}
	seen := map[string]map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".lib") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		entries, err := readImplib(b)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			m := seen[ent.Symbol]
			if m == nil {
				m = map[string]bool{}
				seen[ent.Symbol] = m
			}
			if !m[ent.DLL] {
				m[ent.DLL] = true
				idx.bySym[ent.Symbol] = append(idx.bySym[ent.Symbol], ent.DLL)
			}
		}
	}
	for sym, dlls := range idx.bySym {
		slices.Sort(dlls)
		idx.bySym[sym] = dlls
	}
	return idx, nil
}

// mergeImplibDirs reads one or more import-library directories into a single
// implibIndex. Missing directories are skipped.
func mergeImplibDirs(dirs []string) *implibIndex {
	idx := &implibIndex{bySym: map[string][]string{}}
	seen := map[string]map[string]bool{}
	for _, d := range dirs {
		sub, err := readImplibDir(d)
		if err != nil {
			continue
		}
		for sym, dlls := range sub.bySym {
			m := seen[sym]
			if m == nil {
				m = map[string]bool{}
				seen[sym] = m
			}
			for _, dll := range dlls {
				if !m[dll] {
					m[dll] = true
					idx.bySym[sym] = append(idx.bySym[sym], dll)
				}
			}
		}
	}
	for sym, dlls := range idx.bySym {
		slices.Sort(dlls)
		idx.bySym[sym] = dlls
	}
	return idx
}

// resolve looks up sym in idx. If a single DLL resolves it (directly, by
// classicDLLs precedence, or by implibTieBreak), it returns (dll, lazy, nil)
// with lazy = !isClassicDLL(dll). If multiple DLLs remain ambiguous, it
// returns ("", false, candidates). If no library exports sym, it returns
// ("", false, nil).
func (idx *implibIndex) resolve(sym string) (dll string, lazy bool, ambig []string) {
	if idx == nil {
		return "", false, nil
	}
	cands := idx.bySym[sym]
	switch len(cands) {
	case 0:
		return "", false, nil
	case 1:
		return cands[0], !isClassicDLL(cands[0]), nil
	}
	for _, c := range classicDLLs {
		if slices.Contains(cands, c) {
			return c, false, nil
		}
	}
	drop := map[string]bool{}
	for _, tb := range implibTieBreak {
		if slices.Contains(cands, tb.winner) && slices.Contains(cands, tb.loser) {
			drop[tb.loser] = true
		}
	}
	if len(drop) > 0 {
		var rem []string
		for _, d := range cands {
			if !drop[d] {
				rem = append(rem, d)
			}
		}
		if len(rem) == 1 {
			return rem[0], !isClassicDLL(rem[0]), nil
		}
		if len(rem) > 1 {
			return "", false, rem
		}
	}
	return "", false, slices.Clone(cands)
}

// windowsSDKLibDir returns the x64 import-library directory
// (Lib\<version>\um\x64) matching windowsSDKInclude(root), or "" when absent.
func windowsSDKLibDir(root string) string {
	inc, ver, err := windowsSDKInclude(root)
	if err != nil {
		return ""
	}
	libDir := filepath.Join(filepath.Dir(filepath.Dir(inc)), "Lib", ver, "um", "x64")
	if isDir(libDir) {
		return libDir
	}
	return ""
}

// sdkLibDirs derives the x64 import-library directories corresponding to
// sysdirs (Windows SDK Include\<version>\um directories).
func sdkLibDirs(sysdirs []string) []string {
	var out []string
	for _, d := range sysdirs {
		if !strings.EqualFold(filepath.Base(d), "um") {
			continue
		}
		verDir := filepath.Dir(d)
		incDir := filepath.Dir(verDir)
		kitDir := filepath.Dir(incDir)
		cand := filepath.Join(kitDir, "Lib", filepath.Base(verDir), "um", "x64")
		if isDir(cand) && !slices.Contains(out, cand) {
			out = append(out, cand)
		}
	}
	return out
}
