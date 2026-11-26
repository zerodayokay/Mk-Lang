// Why: the runtime's deadline blocks must pass on every I/O backend and at
// low and high carrier counts.
// What: TestRt_DeadlineBlocks and TestRt_DeadlineBlocksLinux, the RT8- blocks
// of std/task/tests_deadline.mk.
// How: at 1 and 16 carriers, dev and release, natively and on x86_64-linux in
// WSL with MK_IO=auto (io_uring) and epoll, through rt-sched_harness_test.go.

package std

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rt8Carriers are the carrier counts the Deadlines row names.
var rt8Carriers = []int{1, 16}

// rt8Blocks are the rts blocks whose names start with "RT8-".
func rt8Blocks(all []rtsBlock) []rtsBlock {
	var keep []rtsBlock
	for _, b := range all {
		if strings.HasPrefix(b.name, "RT8-") {
			keep = append(keep, b)
		}
	}
	return keep
}

// TestRt_DeadlineBlocks runs every RT8- block at 1 and 16 carriers, dev and
// release, on this host, each under a 60 s watchdog. RTS_ONLY limits both
// tests to the matching blocks. On Windows the RT8-Unix- blocks run once
// more under a TEMP past sun_path (unixLongTemp): they name their sockets
// relative to the working directory, so its length cannot reach them.
func TestRt_DeadlineBlocks(t *testing.T) {
	rtsNative(t)
	for _, profile := range []string{"dev", "release"} {
		exe, all := rtsBuild(t, profile)
		blocks := rtsOnly(t, rt8Blocks(all))
		if len(blocks) < 10 && os.Getenv("RTS_ONLY") == "" {
			t.Fatalf("%d RT8- blocks, want at least 10", len(blocks))
		}
		for _, b := range blocks {
			for _, n := range rt8Carriers {
				b, n := b, n
				t.Run(fmt.Sprintf("%s/%s/carriers=%d", profile, b.name, n), func(t *testing.T) {
					r := rtsRun(t, exe, b.index, 60*time.Second, append(rtsEnv(b), fmt.Sprintf("MK_CARRIERS=%d", n))...)
					rtsJudge(t, b, r, "")
				})
			}
			if runtime.GOOS == "windows" && strings.HasPrefix(b.name, "RT8-Unix-") {
				t.Run(fmt.Sprintf("%s/%s/long TEMP", profile, b.name), func(t *testing.T) {
					long := unixLongTemp(t)
					r := rtsRun(t, exe, b.index, 60*time.Second, append(rtsEnv(b), "MK_CARRIERS=1", "TEMP="+long, "TMP="+long)...)
					rtsJudge(t, b, r, "")
				})
			}
		}
	}
}

// TestRt_DeadlineBlocksLinux runs the same blocks on x86_64-linux, dev and
// release, at 1 and 16 carriers, with MK_IO=auto and MK_IO=epoll: one shell
// script per profile, one wsl.exe client, each case under `timeout -k 5 60`.
// MK_LINUX=1, under tools/wsl-lock.ps1 with go test -p 1. The blocks read
// no file, so no ring read touches /mnt/v.
func TestRt_DeadlineBlocksLinux(t *testing.T) {
	linuxGate(t)
	rtsNeedWSL(t)
	for _, profile := range []string{"linux-dev", "linux-release"} {
		exe, all := rtsBuild(t, profile)
		blocks := rtsOnly(t, rt8Blocks(all))
		dir := filepath.Dir(exe)
		var sh strings.Builder
		sh.WriteString("#!/bin/sh\ncd \"$(dirname \"$0\")\"\n")
		for _, b := range blocks {
			for _, n := range rt8Carriers {
				for _, io := range []string{"auto", "epoll"} {
					// The shared line: status, then start and end stamps.
					sh.WriteString(rtsLinuxCase(strings.Join(rtsEnv(b), " "), n, io, filepath.Base(exe), b.index, fmt.Sprintf("rt8err-%d-%d-%s.txt", b.index, n, io)))
				}
			}
		}
		script := filepath.Join(dir, "rt8run.sh")
		if err := os.WriteFile(script, []byte(sh.String()), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		wslGuard()
		cmd := exec.CommandContext(ctx, "wsl.exe", "-e", "sh", rtsWSLPath(script))
		cmd.WaitDelay = 10 * time.Second
		out, err := cmd.Output()
		cancel()
		if err != nil {
			t.Fatalf("wsl.exe: %v\n%s", err, out)
		}
		got := rtsLinuxLines(string(out))
		for _, b := range blocks {
			for _, n := range rt8Carriers {
				for _, io := range []string{"auto", "epoll"} {
					b, n, io := b, n, io
					t.Run(fmt.Sprintf("%s/%s/carriers=%d/io=%s", profile, b.name, n, io), func(t *testing.T) {
						l, ok := got[fmt.Sprintf("%d %d %s", b.index, n, io)]
						if !ok {
							t.Fatal("no result line")
						}
						code := l.status
						r := rtsResult{timeout: code == "124" || code == "137", took: l.took}
						r.code, _ = strconv.Atoi(code)
						e, _ := os.ReadFile(filepath.Join(dir, fmt.Sprintf("rt8err-%d-%d-%s.txt", b.index, n, io)))
						r.stderr = string(e)
						rtsJudge(t, b, r, "")
					})
				}
			}
		}
	}
}
