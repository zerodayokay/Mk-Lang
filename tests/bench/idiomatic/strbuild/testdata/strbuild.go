// strbuild, Go, idiomatic std: the task of ../README.md with strings.Builder
// growing from empty per document, appending literals, slices, repeated chunks
// and formatted integers via strconv.AppendInt into a stack scratch buffer,
// then hashing the built bytes with FNV-1a 64 plus their length. Built with
// GOAMD64=v2 -pgo=off.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

var words = [16]string{
	"alpha", "bravo", "charlie", "delta",
	"echo", "foxtrot", "golf", "hotel",
	"india", "juliet", "kilo", "lima",
	"mike", "november", "oscar", "papa",
}

const src = "the quick brown fox jumps over the lazy dog 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ "

func fnv(s string) uint64 {
	var h uint64 = 0xcbf29ce484222325
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 0x100000001b3
	}
	return h
}

func buildDoc(d, lines int) uint64 {
	var b strings.Builder
	var num [24]byte
	for i := 0; i < lines; i++ {
		x := d*131 + i*17
		b.WriteString("rec[")
		b.Write(strconv.AppendInt(num[:0], int64(i), 10))
		b.WriteString("] ")
		b.WriteString(words[x&15])
		b.WriteString(":")
		off := (x * 7) % 40
		slen := 8 + (x % 25)
		b.WriteString(src[off : off+slen])
		reps := (i & 3) + 1
		for range reps {
			b.WriteString("-=")
		}
		b.WriteString(" #")
		b.Write(strconv.AppendInt(num[:0], int64(x), 10))
		b.WriteString("\n")
	}
	s := b.String()
	return fnv(s) + uint64(len(s))
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: strbuild <docs> <lines>")
		os.Exit(2)
	}
	docs, _ := strconv.Atoi(os.Args[1])
	lines, _ := strconv.Atoi(os.Args[2])
	var total uint64
	for d := 0; d < docs; d++ {
		total += buildDoc(d, lines)
	}
	fmt.Println(total)
}
