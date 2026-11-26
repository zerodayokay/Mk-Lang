// Why: the COFF import-library reader and ambiguity resolver must work
// deterministically across platforms without Windows SDK or llvm-readobj;
// CI runners lack the SDK, so fake archives test every rule and a
// gated test checks the real SDK when MK_CIMPORT_WINDOWS_SDK is set.
//
// What: makeShortImportLib, TestImplib_ParseAndFilter, TestImplib_Precedence,
// and TestImplib_RealSDK.
//
// How: makeShortImportLib writes standard COFF `!<arch>\n` archives with
// optional `//` longnames and short `IMPORT_OBJECT_HEADER` members.

package driver

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// shortImportSpec describes one member for makeShortImportLib.
type shortImportSpec struct {
	memberName string // archive member name (uses // longnames if >15 chars)
	sym        string
	dll        string
	impType    uint16 // 0=code, 1=data, 2=const
	nameType   uint16 // 0=ordinal, 1=name, 2=noprefix, 3=undecorate
	ordinal    uint16
}

// makeShortImportLib builds a valid COFF import library (`!<arch>\n`) from
// specs, using `//` longnames whenever a memberName exceeds 15 bytes.
func makeShortImportLib(specs []shortImportSpec) []byte {
	var longTab []byte
	type item struct {
		hdrName string
		payload []byte
	}
	var items []item
	for _, s := range specs {
		hdrName := s.memberName + "/"
		if len(hdrName) > 16 {
			off := len(longTab)
			longTab = append(longTab, s.memberName...)
			longTab = append(longTab, 0)
			hdrName = "/" + string(itoaBytes(off))
		}
		var body []byte
		body = binary.LittleEndian.AppendUint16(body, 0)      // Sig1
		body = binary.LittleEndian.AppendUint16(body, 0xFFFF) // Sig2
		body = binary.LittleEndian.AppendUint16(body, 0)      // Version
		body = binary.LittleEndian.AppendUint16(body, 0x8664) // Machine AMD64
		body = binary.LittleEndian.AppendUint32(body, 0)      // TimeDateStamp
		sz := len(s.sym) + 1 + len(s.dll) + 1
		body = binary.LittleEndian.AppendUint32(body, uint32(sz))
		body = binary.LittleEndian.AppendUint16(body, s.ordinal)
		flags := (s.impType & 0x3) | ((s.nameType & 0x7) << 2)
		body = binary.LittleEndian.AppendUint16(body, flags)
		body = append(body, s.sym...)
		body = append(body, 0)
		body = append(body, s.dll...)
		body = append(body, 0)
		items = append(items, item{hdrName: hdrName, payload: body})
	}
	var out []byte
	out = append(out, "!<arch>\n"...)
	// Include a COFF symbol-table "/" member to exercise symbol-table skipping.
	out = append(out, arHeader("/", 4)...)
	out = append(out, 0, 0, 0, 0)
	if len(longTab) > 0 {
		out = append(out, arHeader("//", len(longTab))...)
		out = append(out, longTab...)
		if len(longTab)%2 == 1 {
			out = append(out, '\n')
		}
	}
	for _, it := range items {
		out = append(out, arHeader(it.hdrName, len(it.payload))...)
		out = append(out, it.payload...)
		if len(it.payload)%2 == 1 {
			out = append(out, '\n')
		}
	}
	return out
}

func itoaBytes(n int) []byte {
	if n == 0 {
		return []byte{'0'}
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return b[i:]
}

func TestImplib_ParseAndFilter(t *testing.T) {
	// Longnames, mixed case DLL, ordinal-hinted named export (like d3d12.lib),
	// data export (ignored), C++ mangled export (ignored).
	lib := makeShortImportLib([]shortImportSpec{
		{memberName: "very_long_member_name_one.dll", sym: "D3D12CreateDevice", dll: "D3D12.DLL", impType: 0, nameType: 0, ordinal: 101},
		{memberName: "very_long_member_name_two.dll", sym: "D3D12GetInterface", dll: "d3d12.dll", impType: 0, nameType: 1},
		{memberName: "data.dll", sym: "IID_IFoo", dll: "d3d12.dll", impType: 1, nameType: 1},
		{memberName: "cpp.dll", sym: "?CppFn@@YAXXZ", dll: "d3d12.dll", impType: 0, nameType: 3},
	})
	ents, err := readImplib(lib)
	if err != nil {
		t.Fatal(err)
	}
	want := []implibEntry{
		{Symbol: "D3D12CreateDevice", DLL: "d3d12.dll"},
		{Symbol: "D3D12GetInterface", DLL: "d3d12.dll"},
	}
	if !slices.Equal(ents, want) {
		t.Fatalf("got %+v, want %+v", ents, want)
	}

	// Umbrella library naming >3 distinct DLLs must be ignored completely.
	umb := makeShortImportLib([]shortImportSpec{
		{memberName: "a", sym: "FnA", dll: "a.dll", nameType: 1},
		{memberName: "b", sym: "FnB", dll: "b.dll", nameType: 1},
		{memberName: "c", sym: "FnC", dll: "c.dll", nameType: 1},
		{memberName: "d", sym: "FnD", dll: "d.dll", nameType: 1},
	})
	ents, err = readImplib(umb)
	if err != nil || len(ents) != 0 {
		t.Fatalf("umbrella: got %+v, %v; want empty", ents, err)
	}
}

func TestImplib_Precedence(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, specs []shortImportSpec) {
		if err := os.WriteFile(filepath.Join(dir, name), makeShortImportLib(specs), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("kernel32.lib", []shortImportSpec{
		{memberName: "k1", sym: "QueryPerformanceCounter", dll: "KERNEL32.dll", nameType: 1},
		{memberName: "k2", sym: "GetCurrentProcessId", dll: "KERNEL32.dll", nameType: 1},
	})
	write("vertdll.lib", []shortImportSpec{
		{memberName: "v1", sym: "QueryPerformanceCounter", dll: "vertdll.dll", nameType: 1},
		{memberName: "v2", sym: "RtlGetSystemGlobalData", dll: "vertdll.dll", nameType: 1},
	})
	write("ntdll.lib", []shortImportSpec{
		{memberName: "n1", sym: "RtlGetSystemGlobalData", dll: "ntdll.dll", nameType: 1},
	})
	write("bcrypt.lib", []shortImportSpec{
		{memberName: "b1", sym: "BCryptGenRandom", dll: "bcrypt.dll", nameType: 1},
	})
	write("ncrypt.lib", []shortImportSpec{
		{memberName: "nc1", sym: "BCryptGenRandom", dll: "ncrypt.dll", nameType: 1},
	})
	write("foo.lib", []shortImportSpec{
		{memberName: "f1", sym: "AmbigFn", dll: "foo.dll", nameType: 1},
	})
	write("bar.lib", []shortImportSpec{
		{memberName: "b1", sym: "AmbigFn", dll: "bar.dll", nameType: 1},
	})
	// Umbrella library should not affect resolution.
	write("OneCore.lib", []shortImportSpec{
		{memberName: "u1", sym: "OnlyUmbrella", dll: "a.dll", nameType: 1},
		{memberName: "u2", sym: "X2", dll: "b.dll", nameType: 1},
		{memberName: "u3", sym: "X3", dll: "c.dll", nameType: 1},
		{memberName: "u4", sym: "X4", dll: "d.dll", nameType: 1},
	})

	idx, err := readImplibDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dll, lazy, amb := idx.resolve("QueryPerformanceCounter"); dll != "kernel32.dll" || lazy || len(amb) != 0 {
		t.Errorf("QueryPerformanceCounter = (%q, %v, %v)", dll, lazy, amb)
	}
	if dll, lazy, amb := idx.resolve("BCryptGenRandom"); dll != "bcrypt.dll" || !lazy || len(amb) != 0 {
		t.Errorf("BCryptGenRandom = (%q, %v, %v)", dll, lazy, amb)
	}
	if dll, lazy, amb := idx.resolve("RtlGetSystemGlobalData"); dll != "ntdll.dll" || !lazy || len(amb) != 0 {
		t.Errorf("RtlGetSystemGlobalData = (%q, %v, %v)", dll, lazy, amb)
	}
	if dll, lazy, amb := idx.resolve("AmbigFn"); dll != "" || lazy || !slices.Equal(amb, []string{"bar.dll", "foo.dll"}) {
		t.Errorf("AmbigFn = (%q, %v, %v)", dll, lazy, amb)
	}
	if dll, lazy, amb := idx.resolve("OnlyUmbrella"); dll != "" || lazy || len(amb) != 0 {
		t.Errorf("OnlyUmbrella = (%q, %v, %v)", dll, lazy, amb)
	}
}

func TestImplib_RealSDK(t *testing.T) {
	root := os.Getenv("MK_CIMPORT_WINDOWS_SDK")
	if root == "" {
		t.Skip("MK_CIMPORT_WINDOWS_SDK is not set")
	}
	libDir := windowsSDKLibDir(root)
	if libDir == "" {
		t.Skipf("no Lib\\<version>\\um\\x64 for %s", root)
	}
	idx, err := readImplibDir(libDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sym  string
		dll  string
		lazy bool
	}{
		{"GetCurrentProcessId", "kernel32.dll", false},
		{"CreateWindowExW", "user32.dll", false},
		{"QueryPerformanceCounter", "kernel32.dll", false},
		{"CreateDXGIFactory2", "dxgi.dll", true},
		{"D3D12CreateDevice", "d3d12.dll", true},
		{"D3DCompile", "d3dcompiler_47.dll", true},
		{"BCryptGenRandom", "bcrypt.dll", true},
		{"AcceptEx", "mswsock.dll", true},
		{"RpcIfIdVectorFree", "rpcrt4.dll", true},
		{"RtlGetSystemGlobalData", "ntdll.dll", true},
	} {
		dll, lazy, amb := idx.resolve(tc.sym)
		if dll != tc.dll || lazy != tc.lazy || len(amb) != 0 {
			t.Errorf("%s: got (%q, lazy=%v, amb=%v), want (%q, lazy=%v, nil)", tc.sym, dll, lazy, amb, tc.dll, tc.lazy)
		}
	}
}
