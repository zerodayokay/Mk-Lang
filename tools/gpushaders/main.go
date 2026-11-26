// Why: std.gpu tests need precompiled SPIR-V and DXIL shader blobs so tests
// run on every machine without installing DXC, while staying pinned to a
// single verified DXC version and HLSL source hashes.
//
// What: command `gpushaders` compiling HLSL sources under tools/gpushaders/hlsl/
// with DXC v1.9.2609 into std/gpu/tests_shaders.mk as deflate+base64 constants
// with HLSL, SPIR-V, DXIL and DXC-binary SHA-256 hashes.
//
// How: verifies `dxc --version`, hashes dxc.exe/dxcompiler.dll/dxil.dll, runs
// DXC for DXIL and SPIR-V (vulkan1.3) per shader entry, compresses each blob
// with raw DEFLATE + Base64, and writes std/gpu/tests_shaders.mk deterministically.

package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const wantDxcVersion = "dxcompiler.dll: 1.9(5512-01b62ad4)(1.9.2609.5) - 1.9.2609.5 (01b62ad47)"

type shaderSpec struct {
	ConstName string
	File      string
	Stage     string // "vs", "ps", or "cs"
	Entry     string
}

var shaderSpecs = []shaderSpec{
	{"TriVs", "tri.hlsl", "vs", "VsMain"},
	{"TriPs", "tri.hlsl", "ps", "PsMain"},
	{"QuadVs", "quad.hlsl", "vs", "VsMain"},
	{"QuadPs", "quad.hlsl", "ps", "PsMain"},
	{"TexturedQuadVs", "textured_quad.hlsl", "vs", "VsMain"},
	{"TexturedQuadPs", "textured_quad.hlsl", "ps", "PsMain"},
	{"DepthCubeVs", "depth_cube.hlsl", "vs", "VsMain"},
	{"DepthCubePs", "depth_cube.hlsl", "ps", "PsMain"},
	{"InstancedVs", "instanced.hlsl", "vs", "VsMain"},
	{"InstancedPs", "instanced.hlsl", "ps", "PsMain"},
	{"BlendMsaaVs", "blend_msaa.hlsl", "vs", "VsMain"},
	{"BlendMsaaPs", "blend_msaa.hlsl", "ps", "PsMain"},
	{"DepthSampleVs", "depth_sample.hlsl", "vs", "VsMain"},
	{"DepthSamplePs", "depth_sample.hlsl", "ps", "PsMain"},
	{"DoubleCs", "double.hlsl", "cs", "CsMain"},
	{"PostInvertCs", "post_invert.hlsl", "cs", "CsMain"},
	{"PrefixSumCs", "prefix_sum.hlsl", "cs", "CsMain"},
}

type compiledShader struct {
	Spec        shaderSpec
	HlslSha256  string
	SpirvZ      string
	SpirvSha256 string
	DxilZ       string
	DxilSha256  string
}

type dxcPin struct {
	Version          string
	DxcExeSha256     string
	DxcompilerSha256 string
	DxilSha256       string
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileSha256(path string, normalizeLF bool) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if normalizeLF {
		b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	}
	return sha256Hex(b), nil
}

func deflateBase64(raw []byte) (string, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func verifyDxc(dxcPath string) (dxcPin, error) {
	out, err := exec.Command(dxcPath, "--version").CombinedOutput()
	if err != nil {
		return dxcPin{}, fmt.Errorf("dxc --version: %w\n%s", err, string(out))
	}
	ver := strings.TrimSpace(string(out))
	if ver != wantDxcVersion {
		return dxcPin{}, fmt.Errorf("dxc version mismatch: got %q, want %q", ver, wantDxcVersion)
	}
	dir := filepath.Dir(dxcPath)
	exeSum, err := fileSha256(dxcPath, false)
	if err != nil {
		return dxcPin{}, err
	}
	compSum, err := fileSha256(filepath.Join(dir, "dxcompiler.dll"), false)
	if err != nil {
		return dxcPin{}, err
	}
	dxilSum, err := fileSha256(filepath.Join(dir, "dxil.dll"), false)
	if err != nil {
		return dxcPin{}, err
	}
	return dxcPin{
		Version:          ver,
		DxcExeSha256:     exeSum,
		DxcompilerSha256: compSum,
		DxilSha256:       dxilSum,
	}, nil
}

func compileEntry(dxcPath, hlslDir, tmpDir string, spec shaderSpec) (compiledShader, error) {
	srcPath := filepath.Join(hlslDir, spec.File)
	hlslSum, err := fileSha256(srcPath, true)
	if err != nil {
		return compiledShader{}, err
	}
	profile := spec.Stage + "_6_0"
	dxilOut := filepath.Join(tmpDir, spec.ConstName+".dxil")
	spvOut := filepath.Join(tmpDir, spec.ConstName+".spv")

	dxilArgs := []string{"-T", profile, "-E", spec.Entry, "-O3", "-Zpr", "-Qstrip_debug", "-Fo", dxilOut, srcPath}
	if out, err := exec.Command(dxcPath, dxilArgs...).CombinedOutput(); err != nil {
		return compiledShader{}, fmt.Errorf("dxc dxil %s (%s): %w\n%s", spec.ConstName, spec.File, err, string(out))
	}

	spvArgs := []string{
		"-T", profile,
		"-E", spec.Entry,
		"-O3", "-Zpr",
		"-spirv",
		"-fspv-target-env=vulkan1.3",
		"-fvk-use-dx-layout",
		"-fvk-support-nonzero-base-vertex",
		"-fvk-support-nonzero-base-instance",
	}
	if spec.Stage == "ps" {
		spvArgs = append(spvArgs, "-fvk-use-dx-position-w")
	}
	spvArgs = append(spvArgs, "-Fo", spvOut, srcPath)
	if out, err := exec.Command(dxcPath, spvArgs...).CombinedOutput(); err != nil {
		return compiledShader{}, fmt.Errorf("dxc spirv %s (%s): %w\n%s", spec.ConstName, spec.File, err, string(out))
	}

	dxilBytes, err := os.ReadFile(dxilOut)
	if err != nil {
		return compiledShader{}, err
	}
	spvBytes, err := os.ReadFile(spvOut)
	if err != nil {
		return compiledShader{}, err
	}
	dxilZ, err := deflateBase64(dxilBytes)
	if err != nil {
		return compiledShader{}, err
	}
	spvZ, err := deflateBase64(spvBytes)
	if err != nil {
		return compiledShader{}, err
	}
	return compiledShader{
		Spec:        spec,
		HlslSha256:  hlslSum,
		SpirvZ:      spvZ,
		SpirvSha256: sha256Hex(spvBytes),
		DxilZ:       dxilZ,
		DxilSha256:  sha256Hex(dxilBytes),
	}, nil
}

func generateMk(dxcPath, hlslDir string) ([]byte, error) {
	pin, err := verifyDxc(dxcPath)
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "gpushaders-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	compiled := make([]compiledShader, 0, len(shaderSpecs))
	for _, spec := range shaderSpecs {
		c, err := compileEntry(dxcPath, hlslDir, tmpDir, spec)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, c)
	}

	var out bytes.Buffer
	out.WriteString("// Why: std.gpu tests need verified SPIR-V and DXIL shader blobs without\n")
	out.WriteString("// requiring DXC on every machine.\n")
	out.WriteString("// What: deflate+base64 shader blobs generated by tools/gpushaders from\n")
	out.WriteString("// tools/gpushaders/hlsl/*.hlsl, with SHA-256 verification tests.\n")
	out.WriteString("// How: generated file; DecodeShader decodes Base64 and inflates DEFLATE;\n")
	out.WriteString("// the test block checks every blob's magic header and SHA-256.\n\n")
	out.WriteString("import std.compress\n")
	out.WriteString("import std.encoding\n")
	out.WriteString("import std.hash\n\n")
	fmt.Fprintf(&out, "const ShaderDxcVersion: str = %q\n", pin.Version)
	fmt.Fprintf(&out, "const ShaderDxcExeSha256: str = %q\n", pin.DxcExeSha256)
	fmt.Fprintf(&out, "const ShaderDxcompilerDllSha256: str = %q\n", pin.DxcompilerSha256)
	fmt.Fprintf(&out, "const ShaderDxilDllSha256: str = %q\n\n", pin.DxilSha256)

	for _, c := range compiled {
		fmt.Fprintf(&out, "const %sHlslSha256: str = %q\n", c.Spec.ConstName, c.HlslSha256)
		fmt.Fprintf(&out, "const %sSpirvZ: str = %q\n", c.Spec.ConstName, c.SpirvZ)
		fmt.Fprintf(&out, "const %sSpirvSha256: str = %q\n", c.Spec.ConstName, c.SpirvSha256)
		fmt.Fprintf(&out, "const %sDxilZ: str = %q\n", c.Spec.ConstName, c.DxilZ)
		fmt.Fprintf(&out, "const %sDxilSha256: str = %q\n\n", c.Spec.ConstName, c.DxilSha256)
	}

	out.WriteString("DecodeShader(a: &Arena, z_b64: str): compress.Error|encoding.Error![]u8 from a {\n")
	out.WriteString("    raw := try encoding.Base64Decode(a, z_b64, encoding.Base64)\n")
	out.WriteString("    return try compress.Inflate(a, raw, 4 * 1024 * 1024)\n")
	out.WriteString("}\n\n")
	out.WriteString("CheckShaderBlob(a: &Arena, z_b64: str, want_sha: str, want_magic: u32): compress.Error|encoding.Error!void {\n")
	out.WriteString("    blob := try DecodeShader(a, z_b64)\n")
	out.WriteString("    Assert(blob.Len() >= 4)\n")
	out.WriteString("    magic := u32(blob[0]) | (u32(blob[1]) << 8) | (u32(blob[2]) << 16) | (u32(blob[3]) << 24)\n")
	out.WriteString("    Assert(magic == want_magic)\n")
	out.WriteString("    sum := hash.Sha256(blob)\n")
	out.WriteString("    hex := try encoding.HexEncode(a, sum[..])\n")
	out.WriteString("    Assert(hex.Eq(&want_sha))\n")
	out.WriteString("}\n\n")
	out.WriteString("test \"shader blobs inflate and match SHA-256 and magic headers\" (sys: &System) {\n")
	out.WriteString("    var ar := try Arena.New(sys.heap)\n")
	out.WriteString("    defer ar.Free()\n")
	for _, c := range compiled {
		fmt.Fprintf(&out, "    try CheckShaderBlob(&ar, %sSpirvZ, %sSpirvSha256, 0x07230203)\n", c.Spec.ConstName, c.Spec.ConstName)
		fmt.Fprintf(&out, "    try CheckShaderBlob(&ar, %sDxilZ, %sDxilSha256, 0x43425844)\n", c.Spec.ConstName, c.Spec.ConstName)
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}

func main() {
	dxcPath := flag.String("dxc", "", "path to pinned dxc.exe")
	hlslDir := flag.String("hlsl", "tools/gpushaders/hlsl", "directory containing HLSL sources")
	outPath := flag.String("out", "std/gpu/tests_shaders.mk", "output Mk file")
	flag.Parse()

	if *dxcPath == "" {
		fmt.Fprintln(os.Stderr, "gpushaders: -dxc <path> is required")
		os.Exit(2)
	}
	data, err := generateMk(*dxcPath, *hlslDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gpushaders: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "gpushaders: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gpushaders: %v\n", err)
		os.Exit(1)
	}
}
