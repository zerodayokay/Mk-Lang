// utf8scan, Go, idiomatic std: the task of ../README.md with unicode/utf8.Valid
// and `for _, r := range string(buf)` (zero-copy rune iteration in gc) over a
// generated mixed-script UTF-8 buffer (ASCII, Latin-1, Greek, Cyrillic, CJK,
// and 4-byte emoji), folding codepoint count and scalar sum each round and
// toggling one ASCII letter between rounds. Built with GOAMD64=v2 -pgo=off.
package main

import (
	"fmt"
	"os"
	"strconv"
	"unicode/utf8"
)

const unit = "The quick brown fox jumps over 42 lazy dogs! Caf\u00e9 na\u00efve r\u00e9sum\u00e9 \u00fcber\n\u0391\u03bb\u03b3\u03cc\u03c1\u03b9\u03b8\u03bc\u03bf\u03c2 \u03ba\u03b1\u03b9 \u041f\u0440\u0438\u0432\u0435\u0442 \u043c\u0438\u0440 2026\n\u65e5\u672c\u8a9e\u30c6\u30ad\u30b9\u30c8 \u4e2d\u6587\u6d4b\u8bd5 \ud55c\uad6d\uc5b4 \u6c34\u306f\u51b7\u305f\u3044\nStatus: OK \U0001f600\U0001f680\U0001f30d\U0001f525\u2728\U0001f4bb\U0001f389 end-of-record.\n"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: utf8scan <n> <rounds>")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[1])
	rounds, _ := strconv.Atoi(os.Args[2])
	ulen := len(unit)
	buf := make([]byte, n*ulen)
	for i := 0; i < n; i++ {
		copy(buf[i*ulen:], unit)
	}
	var total uint64
	for r := 0; r < rounds; r++ {
		if !utf8.Valid(buf) {
			panic("invalid utf-8")
		}
		var count uint64
		var cpsum uint64
		for _, c := range string(buf) {
			count++
			cpsum += uint64(c)
		}
		total += cpsum + count
		idx := (r % n) * ulen
		buf[idx] ^= 0x20
	}
	fmt.Println(total)
}
