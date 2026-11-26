// Why: the system views mk ships are generated from pinned inputs by a
// maintainer, never by a user's build, and a regenerated view must be
// reviewable and its drift from the committed one visible.
//
// What: the sysview command. It generates every system view whose input
// it is given and writes <out>/<target>/<view>.mksv and views.txt; -check
// writes nothing and fails when a committed archive's identity differs.
//
// How: internal/driver's GenerateSystemViews does the work (one translation
// unit per target, the whole of every view checked, failing items dropped
// and listed); this file parses flags, writes the files and prints the
// skipped rows.

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mk/internal/driver"
	"mk/internal/sysview"
)

func main() {
	sdk := flag.String("sdk", "", `the Windows Kits\10 directory, or one Include\<version> directory`)
	vulkan := flag.String("vulkan", "", "Vulkan-Headers' include directory")
	glibcX64 := flag.String("glibc-x86_64", "", "extracted Debian glibc sysroot for x86_64-linux")
	glibcArm64 := flag.String("glibc-aarch64", "", "extracted Debian glibc sysroot for aarch64-linux")
	out := flag.String("out", filepath.Join("internal", "sysview", "data"), "the directory the archives go to, one directory per target")
	check := flag.Bool("check", false, "write nothing; fail when a committed archive differs from the generated one")
	flag.Parse()
	t0 := time.Now()
	res, err := driver.GenerateSystemViews(driver.SysGenOptions{WindowsSDK: *sdk, Vulkan: *vulkan,
		Glibc: map[string]string{"x86_64-linux": *glibcX64, "aarch64-linux": *glibcArm64},
		Log:   func(f string, a ...any) { fmt.Fprintf(os.Stderr, "sysview: "+f+"\n", a...) }})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sysview: %v\n", err)
		os.Exit(1)
	}
	byTarget := map[string][]driver.SysGenView{}
	var targets []string
	for _, v := range res.Views {
		t := v.View.Meta.Target
		if byTarget[t] == nil {
			targets = append(targets, t)
		}
		byTarget[t] = append(byTarget[t], v)
	}
	bad := 0
	for _, t := range targets {
		dir := filepath.Join(*out, t)
		var metas []sysview.Meta
		var ids [][32]byte
		var dropped [][]sysview.Dropped
		var unlinked [][]sysview.Ambiguous
		for _, v := range byTarget[t] {
			a, err := sysview.Open(v.Archive)
			if err != nil {
				fmt.Fprintf(os.Stderr, "sysview: %v\n", err)
				os.Exit(1)
			}
			metas, ids, dropped, unlinked = append(metas, v.View.Meta), append(ids, a.ID()), append(dropped, v.Dropped), append(unlinked, v.Ambiguous)
			p := filepath.Join(dir, sysview.ArchiveName(v.View.Meta.Name))
			if *check {
				old, err := os.ReadFile(p)
				if err != nil {
					fmt.Printf("%s: not committed\n", p)
					bad++
					continue
				}
				oa, err := sysview.Open(old)
				if err != nil || oa.ID() != a.ID() {
					fmt.Printf("%s: differs from the generated view\n", p)
					bad++
				}
				continue
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "sysview: %v\n", err)
				os.Exit(1)
			}
			if err := os.WriteFile(p, v.Archive, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "sysview: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("%s: %d items (%d dropped), %d bytes\n", p, len(v.View.Items), len(v.Dropped), len(v.Archive))
		}
		if !*check {
			text := sysview.ManifestTextUnlinked(t, metas, ids, dropped, unlinked)
			if err := os.WriteFile(filepath.Join(dir, sysview.ManifestName), []byte(text), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "sysview: %v\n", err)
				os.Exit(1)
			}
		}
	}
	for _, s := range res.Skipped {
		fmt.Println(s)
	}
	fmt.Fprintf(os.Stderr, "sysview: done in %.1f s\n", time.Since(t0).Seconds())
	if bad > 0 {
		os.Exit(1)
	}
}
