// Why: committed shader blobs in std/gpu/tests_shaders.mk must match the
// HLSL source hashes and inflate to their recorded SHA-256 digests even when
// DXC is absent, and regenerate byte-for-byte identically when DXC is present.
//
// What: TestCommittedShadersMatchSources and TestRegenerateWithDxc.
//
// How: parses const strings from std/gpu/tests_shaders.mk, compares
// <Name>HlslSha256 against tools/gpushaders/hlsl/, inflates <Name>SpirvZ
// and <Name>DxilZ to check their SHA-256 and magic headers, and runs
// generateMk when DXC exists.

package main

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func parseConsts(t *testing.T, src []byte) map[string]string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^const (\w+): str = "([^"]*)"$`)
	m := re.FindAllSubmatch(src, -1)
	out := make(map[string]string, len(m))
	for _, g := range m {
		out[string(g[1])] = string(g[2])
	}
	return out
}

func inflateCheck(t *testing.T, name, b64, wantSha string, wantMagic uint32) {
	t.Helper()
	rawZ, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("%s base64: %v", name, err)
	}
	r := flate.NewReader(bytes.NewReader(rawZ))
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s inflate: %v", name, err)
	}
	if len(raw) < 4 {
		t.Fatalf("%s too short: %d", name, len(raw))
	}
	if got := binary.LittleEndian.Uint32(raw[:4]); got != wantMagic {
		t.Fatalf("%s magic = 0x%08x, want 0x%08x", name, got, wantMagic)
	}
	if got := sha256Hex(raw); got != wantSha {
		t.Fatalf("%s sha256 = %s, want %s", name, got, wantSha)
	}
}

func TestCommittedShadersMatchSources(t *testing.T) {
	root := repoRoot(t)
	mkPath := filepath.Join(root, "std", "gpu", "tests_shaders.mk")
	src, err := os.ReadFile(mkPath)
	if err != nil {
		t.Fatal(err)
	}
	consts := parseConsts(t, src)
	hlslDir := filepath.Join(root, "tools", "gpushaders", "hlsl")
	for _, spec := range shaderSpecs {
		wantHlsl := consts[spec.ConstName+"HlslSha256"]
		if wantHlsl == "" {
			t.Fatalf("missing %sHlslSha256", spec.ConstName)
		}
		gotHlsl, err := fileSha256(filepath.Join(hlslDir, spec.File), true)
		if err != nil {
			t.Fatal(err)
		}
		if gotHlsl != wantHlsl {
			t.Errorf("%s (%s): HLSL sha256 = %s, recorded %s", spec.ConstName, spec.File, gotHlsl, wantHlsl)
		}
		inflateCheck(t, spec.ConstName+"Spirv", consts[spec.ConstName+"SpirvZ"], consts[spec.ConstName+"SpirvSha256"], 0x07230203)
		inflateCheck(t, spec.ConstName+"Dxil", consts[spec.ConstName+"DxilZ"], consts[spec.ConstName+"DxilSha256"], 0x43425844)
	}
}

func TestRegenerateWithDxc(t *testing.T) {
	root := repoRoot(t)
	dxcPath := os.Getenv("MK_DXC")
	if dxcPath == "" {
		dxcPath = `V:\Mk-wt\_run\tools\dxc\x\bin\x64\dxc.exe`
	}
	if _, err := os.Stat(dxcPath); err != nil {
		t.Skipf("DXC not present at %s", dxcPath)
	}
	got, err := generateMk(dxcPath, filepath.Join(root, "tools", "gpushaders", "hlsl"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(root, "std", "gpu", "tests_shaders.mk"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))) {
		t.Fatalf("std/gpu/tests_shaders.mk differs from regenerated output (%d vs %d bytes)", len(want), len(got))
	}
}
