// Why: std.gpu golden tests need deterministic WARP PNG baselines keyed by
// the d3d10warp.dll version that produced them, with pure-Go generation and
// tolerance-aware verification.
//
// What: command `gpugolden` generating and verifying committed PNG goldens under
// tools/gpugolden/goldens/ and std/gpu/tests_goldens.mk.
//
// How: parses d3d10warp.dll VS_FIXEDFILEINFO via debug/pe (zero cgo/unsafe),
// invokes `mk test --std=std.gpu --run="gpu dump warp goldens"` under
// MK_GPU_ADAPTER=warp to render the 9 scenes, validates PNG decode, writes
// goldens + Base64/SHA-256 constants, and verifies exact match on matching
// WARP version or bounded tolerance on unseen versions.

package main

import (
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type sceneSpec struct {
	Name      string
	ConstName string
	IsMsaa    bool
}

var goldenScenes = []sceneSpec{
	{"triangle", "Triangle", false},
	{"indexed_textured_quad", "IndexedTexturedQuad", false},
	{"depth_cubes", "DepthCubes", false},
	{"instancing", "Instancing", false},
	{"msaa_resolve", "MsaaResolve", true},
	{"blending", "Blending", false},
	{"mip_chain", "MipChain", false},
	{"depth_sample", "DepthSample", false},
	{"compute_post", "ComputePost", false},
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readPEFileVersion(path string) (string, error) {
	f, err := pe.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sec := f.Section(".rsrc")
	if sec == nil {
		return "", fmt.Errorf("missing .rsrc section in %s", path)
	}
	data, err := sec.Data()
	if err != nil {
		return "", err
	}
	rvaBase := sec.VirtualAddress

	readDirEntry := func(off uint32, wantID uint32, first bool) (uint32, bool) {
		if int(off)+16 > len(data) {
			return 0, false
		}
		named := binary.LittleEndian.Uint16(data[off+12 : off+14])
		idCnt := binary.LittleEndian.Uint16(data[off+14 : off+16])
		pos := off + 16 + uint32(named)*8
		for i := uint16(0); i < idCnt; i++ {
			e := pos + uint32(i)*8
			if int(e)+8 > len(data) {
				return 0, false
			}
			id := binary.LittleEndian.Uint32(data[e : e+4])
			val := binary.LittleEndian.Uint32(data[e+4 : e+8])
			if first || id == wantID {
				return val, true
			}
		}
		return 0, false
	}

	// Level 1: RT_VERSION (type 16).
	l2, ok := readDirEntry(0, 16, false)
	if !ok || (l2&0x80000000) == 0 {
		return "", fmt.Errorf("RT_VERSION directory not found in %s", path)
	}
	// Level 2: first resource id.
	l3, ok := readDirEntry(l2&0x7FFFFFFF, 0, true)
	if !ok || (l3&0x80000000) == 0 {
		return "", fmt.Errorf("RT_VERSION id entry not found in %s", path)
	}
	// Level 3: first language leaf.
	leafOff, ok := readDirEntry(l3&0x7FFFFFFF, 0, true)
	if !ok || (leafOff&0x80000000) != 0 {
		return "", fmt.Errorf("RT_VERSION data entry not found in %s", path)
	}
	if int(leafOff)+16 > len(data) {
		return "", fmt.Errorf("RT_VERSION data entry truncated in %s", path)
	}
	dataRVA := binary.LittleEndian.Uint32(data[leafOff : leafOff+4])
	dataSize := binary.LittleEndian.Uint32(data[leafOff+4 : leafOff+8])
	if dataRVA < rvaBase {
		return "", fmt.Errorf("invalid RT_VERSION RVA 0x%x in %s", dataRVA, path)
	}
	start := dataRVA - rvaBase
	if int(start+dataSize) > len(data) || dataSize < 52 {
		return "", fmt.Errorf("RT_VERSION data out of bounds in %s", path)
	}
	res := data[start : start+dataSize]
	sig := []byte{0xBD, 0x04, 0xEF, 0xFE}
	idx := bytes.Index(res, sig)
	if idx < 0 || idx+16 > len(res) {
		return "", fmt.Errorf("VS_FIXEDFILEINFO signature not found in %s", path)
	}
	ms := binary.LittleEndian.Uint32(res[idx+8 : idx+12])
	ls := binary.LittleEndian.Uint32(res[idx+12 : idx+16])
	return fmt.Sprintf("%d.%d.%d.%d", ms>>16, ms&0xFFFF, ls>>16, ls&0xFFFF), nil
}

type rgbaDiff struct {
	max      int
	over     int
	isolated int
	pixels   int
}

func pixelDiff(a, b []byte, x, y, w int) int {
	off := (y*w + x) * 4
	best := 0
	for c := 0; c < 4; c++ {
		va := int(a[off+c])
		vb := int(b[off+c])
		d := va - vb
		if d < 0 {
			d = -d
		}
		if d > best {
			best = d
		}
	}
	return best
}

func toRGBA(img image.Image) (*image.RGBA, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out, nil
}

func comparePNGs(wantPNG, gotPNG []byte, tol int) (rgbaDiff, error) {
	imgA, err := png.Decode(bytes.NewReader(wantPNG))
	if err != nil {
		return rgbaDiff{}, fmt.Errorf("decode want png: %w", err)
	}
	imgB, err := png.Decode(bytes.NewReader(gotPNG))
	if err != nil {
		return rgbaDiff{}, fmt.Errorf("decode got png: %w", err)
	}
	if imgA.Bounds().Dx() != imgB.Bounds().Dx() || imgA.Bounds().Dy() != imgB.Bounds().Dy() {
		return rgbaDiff{}, fmt.Errorf("dimension mismatch: want %dx%d got %dx%d",
			imgA.Bounds().Dx(), imgA.Bounds().Dy(), imgB.Bounds().Dx(), imgB.Bounds().Dy())
	}
	ra, _ := toRGBA(imgA)
	rb, _ := toRGBA(imgB)
	w, h := ra.Bounds().Dx(), ra.Bounds().Dy()
	res := rgbaDiff{pixels: w * h}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := pixelDiff(ra.Pix, rb.Pix, x, y, w)
			if d > res.max {
				res.max = d
			}
			if d > tol {
				res.over++
				hasNbr := (x > 0 && pixelDiff(ra.Pix, rb.Pix, x-1, y, w) <= tol) ||
					(x+1 < w && pixelDiff(ra.Pix, rb.Pix, x+1, y, w) <= tol) ||
					(y > 0 && pixelDiff(ra.Pix, rb.Pix, x, y-1, w) <= tol) ||
					(y+1 < h && pixelDiff(ra.Pix, rb.Pix, x, y+1, w) <= tol)
				if !hasNbr {
					res.isolated++
				}
			}
		}
	}
	return res, nil
}

func dumpWarpPNGs(mkPath, stdDir, outDir string) error {
	cacheDir, err := os.MkdirTemp("", "gpugolden-cache-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cacheDir)

	var cmd *exec.Cmd
	if mkPath != "" {
		cmd = exec.Command(mkPath, "test", "--std=std.gpu", "--run=gpu dump warp goldens")
	} else {
		cmd = exec.Command("go", "run", "./cmd/mk", "test", "--std=std.gpu", "--run=gpu dump warp goldens")
	}
	env := os.Environ()
	env = append(env,
		"MK_STD="+stdDir,
		"MK_CACHE="+cacheDir,
		"MK_GPU_ADAPTER=warp",
		"MK_GPU_DUMP_GOLDENS="+outDir,
	)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("render warp goldens failed: %w\n%s", err, string(out))
	}
	return nil
}

func renderGoldensMk(warpVer string, pngs map[string][]byte) []byte {
	var out bytes.Buffer
	out.WriteString("// Why: the device tests compare rendered scenes with WARP baselines\n")
	out.WriteString("// keyed by the d3d10warp.dll version that produced them.\n")
	out.WriteString("// What: generated by tools/gpugolden (do not edit): GoldenWarpVersion,\n")
	out.WriteString("// each scene's Base64 PNG and SHA-256, DecodeGoldenImage, and a test\n")
	out.WriteString("// that every golden matches its digest and decodes to 64x64.\n")
	out.WriteString("// How: DecodeGoldenImage decodes the Base64, checks the SHA-256, then\n")
	out.WriteString("// decodes the PNG with std.image.\n\n")
	out.WriteString("import std.encoding\n")
	out.WriteString("import std.hash\n")
	out.WriteString("import std.image\n\n")
	fmt.Fprintf(&out, "const GoldenWarpVersion: str = %q\n\n", warpVer)

	for _, sc := range goldenScenes {
		b := pngs[sc.Name]
		b64 := base64.StdEncoding.EncodeToString(b)
		sha := sha256Hex(b)
		fmt.Fprintf(&out, "const Golden%sPngB64: str = %q\n", sc.ConstName, b64)
		fmt.Fprintf(&out, "const Golden%sSha256: str = %q\n\n", sc.ConstName, sha)
	}

	out.WriteString("GoldenScenePngData(idx: int): (b64: str, sha: str) {\n")
	out.WriteString("    return match idx {\n")
	for i, sc := range goldenScenes {
		if i == len(goldenScenes)-1 {
			fmt.Fprintf(&out, "        _ { (b64: Golden%sPngB64, sha: Golden%sSha256) }\n", sc.ConstName, sc.ConstName)
		} else {
			fmt.Fprintf(&out, "        %d { (b64: Golden%sPngB64, sha: Golden%sSha256) }\n", i, sc.ConstName, sc.ConstName)
		}
	}
	out.WriteString("    }\n}\n\n")

	out.WriteString("DecodeGoldenImage(a: &Arena, idx: int): encoding.Error|image.Error!image.Image from a {\n")
	out.WriteString("    (b64, sha) := GoldenScenePngData(idx)\n")
	out.WriteString("    png := try encoding.Base64Decode(a, b64, encoding.Base64)\n")
	out.WriteString("    sum := hash.Sha256(png)\n")
	out.WriteString("    hex := try encoding.HexEncode(a, sum[..])\n")
	out.WriteString("    Assert(hex.Eq(&sha))\n")
	out.WriteString("    return try image.DecodePng(a, png, image.DefaultDecodeLimits)\n")
	out.WriteString("}\n\n")

	out.WriteString("test \"committed WARP golden PNGs decode and match SHA-256\" (sys: &System) {\n")
	out.WriteString("    var ar := try Arena.New(sys.heap)\n")
	out.WriteString("    defer ar.Free()\n")
	out.WriteString("    Assert(GoldenWarpVersion.Len() > 0)\n")
	for i := range goldenScenes {
		fmt.Fprintf(&out, "    g%d := try DecodeGoldenImage(&ar, %d)\n", i, i)
		fmt.Fprintf(&out, "    Assert(g%d.width == 64 && g%d.height == 64)\n", i, i)
		out.WriteString("    ar.Reset()\n")
	}
	out.WriteString("}\n")
	return out.Bytes()
}

func runGenerate(mkPath, warpPath, stdDir, goldensDir, outMk string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("generate requires Windows WARP")
	}
	ver, err := readPEFileVersion(warpPath)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "gpugolden-gen-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if err := dumpWarpPNGs(mkPath, stdDir, tmpDir); err != nil {
		return err
	}
	if err := os.MkdirAll(goldensDir, 0o755); err != nil {
		return err
	}
	pngs := make(map[string][]byte, len(goldenScenes))
	for _, sc := range goldenScenes {
		b, err := os.ReadFile(filepath.Join(tmpDir, sc.Name+".png"))
		if err != nil {
			return err
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("decode %s.png: %w", sc.Name, err)
		}
		if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
			return fmt.Errorf("%s.png size %dx%d, want 64x64", sc.Name, img.Bounds().Dx(), img.Bounds().Dy())
		}
		if err := os.WriteFile(filepath.Join(goldensDir, sc.Name+".png"), b, 0o644); err != nil {
			return err
		}
		pngs[sc.Name] = b
	}
	mkSrc := renderGoldensMk(ver, pngs)
	if err := os.WriteFile(outMk, mkSrc, 0o644); err != nil {
		return err
	}
	fmt.Printf("generated %d goldens for d3d10warp.dll %s -> %s\n", len(goldenScenes), ver, outMk)
	return nil
}

func runVerify(mkPath, warpPath, stdDir, goldensDir, outMk string, live bool) error {
	wantMk, err := os.ReadFile(outMk)
	if err != nil {
		return err
	}
	constVerMarker := `const GoldenWarpVersion: str = "`
	vi := strings.Index(string(wantMk), constVerMarker)
	if vi < 0 {
		return fmt.Errorf("GoldenWarpVersion missing in %s", outMk)
	}
	rest := string(wantMk)[vi+len(constVerMarker):]
	endQ := strings.IndexByte(rest, '"')
	if endQ < 0 {
		return fmt.Errorf("unterminated GoldenWarpVersion in %s", outMk)
	}
	goldenVer := rest[:endQ]

	pngs := make(map[string][]byte, len(goldenScenes))
	for _, sc := range goldenScenes {
		b, err := os.ReadFile(filepath.Join(goldensDir, sc.Name+".png"))
		if err != nil {
			return err
		}
		if _, err := png.Decode(bytes.NewReader(b)); err != nil {
			return fmt.Errorf("invalid committed PNG %s: %w", sc.Name, err)
		}
		pngs[sc.Name] = b
	}
	expectedMk := renderGoldensMk(goldenVer, pngs)
	if !bytes.Equal(bytes.ReplaceAll(expectedMk, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(wantMk, []byte("\r\n"), []byte("\n"))) {
		return fmt.Errorf("%s does not match committed PNGs under %s", outMk, goldensDir)
	}
	if !live || runtime.GOOS != "windows" {
		fmt.Printf("verified %d committed goldens (d3d10warp.dll %s)\n", len(goldenScenes), goldenVer)
		return nil
	}
	hostVer, err := readPEFileVersion(warpPath)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "gpugolden-ver-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := dumpWarpPNGs(mkPath, stdDir, tmpDir); err != nil {
		return err
	}
	exact := hostVer == goldenVer
	if !exact {
		fmt.Printf("WARP version mismatch (golden=%s, host=%s): using hardware tolerance\n", goldenVer, hostVer)
	}
	for _, sc := range goldenScenes {
		got, err := os.ReadFile(filepath.Join(tmpDir, sc.Name+".png"))
		if err != nil {
			return err
		}
		want := pngs[sc.Name]
		if exact {
			if !bytes.Equal(want, got) {
				diff, cerr := comparePNGs(want, got, 0)
				if cerr != nil {
					return fmt.Errorf("%s exact compare: %w", sc.Name, cerr)
				}
				if diff.max != 0 || diff.over != 0 {
					return fmt.Errorf("%s mismatch on matching WARP %s: max=%d over=%d", sc.Name, hostVer, diff.max, diff.over)
				}
			}
		} else {
			diff, cerr := comparePNGs(want, got, 2)
			if cerr != nil {
				return fmt.Errorf("%s tolerance compare: %w", sc.Name, cerr)
			}
			limit := diff.pixels / 1000
			if sc.IsMsaa {
				limit = diff.pixels / 200
			}
			if diff.isolated != 0 || diff.over > limit {
				return fmt.Errorf("%s mismatch (golden=%s host=%s): max=%d over=%d/%d isolated=%d",
					sc.Name, goldenVer, hostVer, diff.max, diff.over, limit, diff.isolated)
			}
		}
	}
	fmt.Printf("verified %d live WARP goldens (golden=%s host=%s)\n", len(goldenScenes), goldenVer, hostVer)
	return nil
}

func main() {
	mode := flag.String("mode", "verify", "generate or verify")
	mkPath := flag.String("mk", "", "path to mk binary")
	warpPath := flag.String("warp", `C:\Windows\System32\d3d10warp.dll`, "path to d3d10warp.dll")
	stdDir := flag.String("std", "std", "path to std directory")
	goldensDir := flag.String("goldens", "tools/gpugolden/goldens", "directory for committed PNG goldens")
	outMk := flag.String("out", "std/gpu/tests_goldens.mk", "output Mk file")
	live := flag.Bool("live", false, "also render live WARP frames during verify")
	flag.Parse()

	var err error
	switch *mode {
	case "generate":
		err = runGenerate(*mkPath, *warpPath, *stdDir, *goldensDir, *outMk)
	case "verify":
		err = runVerify(*mkPath, *warpPath, *stdDir, *goldensDir, *outMk, *live)
	default:
		err = fmt.Errorf("unknown -mode %q (want generate or verify)", *mode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gpugolden: %v\n", err)
		os.Exit(1)
	}
}
