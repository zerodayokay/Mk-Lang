// Why: committed WARP goldens in std/gpu/tests_goldens.mk must stay in sync
// with tools/gpugolden/goldens/*.png, and PE version + pixel-diff helpers
// must behave deterministically.
//
// What: TestCommittedGoldensMatchPngs, TestReadWarpVersion, and
// TestComparePNGs.
//
// How: locates the repo root, runs runVerify(..., live=false) on the committed
// assets, reads d3d10warp.dll version on Windows, and checks synthetic PNG diffs.

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestCommittedGoldensMatchPngs(t *testing.T) {
	root := repoRoot(t)
	err := runVerify(
		"",
		"",
		filepath.Join(root, "std"),
		filepath.Join(root, "tools", "gpugolden", "goldens"),
		filepath.Join(root, "std", "gpu", "tests_goldens.mk"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadWarpVersion(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("d3d10warp.dll is Windows-only")
	}
	path := `C:\Windows\System32\d3d10warp.dll`
	if _, err := os.Stat(path); err != nil {
		t.Skipf("d3d10warp.dll not present: %v", err)
	}
	ver, err := readPEFileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(ver, ".")
	if len(parts) != 4 {
		t.Fatalf("got version %q, want A.B.C.D", ver)
	}
}

func encodeSolidPNG(t *testing.T, w, h int, mutate func(img *image.RGBA)) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 100, G: 100, B: 100, A: 255})
		}
	}
	if mutate != nil {
		mutate(img)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestComparePNGs(t *testing.T) {
	base := encodeSolidPNG(t, 8, 8, nil)
	exact, err := comparePNGs(base, base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if exact.max != 0 || exact.over != 0 || exact.isolated != 0 {
		t.Fatalf("exact diff = %+v", exact)
	}

	edgeImg := encodeSolidPNG(t, 8, 8, func(img *image.RGBA) {
		img.SetRGBA(3, 3, color.RGBA{R: 110, G: 100, B: 100, A: 255})
	})
	edgeDiff, err := comparePNGs(base, edgeImg, 2)
	if err != nil {
		t.Fatal(err)
	}
	if edgeDiff.over != 1 || edgeDiff.isolated != 0 {
		t.Fatalf("edge diff = %+v, want over=1 isolated=0", edgeDiff)
	}

	isolatedImg := encodeSolidPNG(t, 8, 8, func(img *image.RGBA) {
		for _, pt := range [][2]int{{3, 3}, {2, 3}, {4, 3}, {3, 2}, {3, 4}} {
			img.SetRGBA(pt[0], pt[1], color.RGBA{R: 110, G: 100, B: 100, A: 255})
		}
	})
	isoDiff, err := comparePNGs(base, isolatedImg, 2)
	if err != nil {
		t.Fatal(err)
	}
	if isoDiff.over != 5 || isoDiff.isolated != 1 {
		t.Fatalf("isolated diff = %+v, want over=5 isolated=1", isoDiff)
	}
}
