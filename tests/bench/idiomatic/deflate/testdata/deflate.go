// deflate, Go, idiomatic std: the task of ../README.md with compress/flate
// (DefaultCompression, reusable Writer and Resetter Reader) round-tripping
// a generated mixed text buffer in memory. Each round verifies exact output
// length and FNV-1a 64 checksum against the source buffer and flips one
// source byte between rounds. Built with GOAMD64=v2 -pgo=off.
package main

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
	"os"
	"strconv"
)

const hex = "0123456789abcdef"

var vocab = [16]string{
	"ERROR", "WARN ", "INFO ", "DEBUG",
	"GET", "POST", "PUT", "DELETE",
	"/api/v1/users", "/api/v1/orders", "/api/v1/items", "/healthz",
	"timeout", "ok", "cached", "retry",
}

const prose = "The quick brown fox jumps over the lazy dog; pack my box with five dozen liquor jugs! 0123456789 "

func mix(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func fnv(b []byte) uint64 {
	var h uint64 = 0xcbf29ce484222325
	for _, c := range b {
		h = (h ^ uint64(c)) * 0x100000001b3
	}
	return h
}

func emit(dst []byte, pos *int, s string) {
	rem := len(dst) - *pos
	if rem <= 0 {
		return
	}
	k := len(s)
	if k > rem {
		k = rem
	}
	copy(dst[*pos:*pos+k], s[:k])
	*pos += k
}

func generate(dst []byte, seed uint64) {
	st := seed
	pos := 0
	n := len(dst)
	for pos < n {
		st += 0x9e3779b97f4a7c15
		z := mix(st)
		emit(dst, &pos, "ts=")
		for k := 0; k < 8; k++ {
			if pos < n {
				shift := uint(60 - 4*k)
				dst[pos] = hex[(z>>shift)&15]
				pos++
			}
		}
		emit(dst, &pos, " level=")
		emit(dst, &pos, vocab[z&3])
		emit(dst, &pos, " method=")
		emit(dst, &pos, vocab[4+((z>>2)&3)])
		emit(dst, &pos, " path=")
		emit(dst, &pos, vocab[8+((z>>4)&3)])
		emit(dst, &pos, " msg=")
		emit(dst, &pos, vocab[12+((z>>6)&3)])
		emit(dst, &pos, " body=")
		off := int((z >> 8) % 60)
		plen := 16 + int((z>>16)%21)
		emit(dst, &pos, prose[off:off+plen])
		emit(dst, &pos, "\n")
	}
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: deflate <bytes> <rounds> <seed>")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[1])
	rounds, _ := strconv.Atoi(os.Args[2])
	seed, _ := strconv.ParseUint(os.Args[3], 10, 64)
	src := make([]byte, n)
	generate(src, seed)
	var compBuf bytes.Buffer
	compBuf.Grow(n + n>>4 + 1<<20)
	zw, err := flate.NewWriter(&compBuf, flate.DefaultCompression)
	if err != nil {
		panic(err)
	}
	decBuf := make([]byte, n+64)
	var rd bytes.Reader
	zr := flate.NewReader(&rd)
	var total uint64
	for r := 0; r < rounds; r++ {
		want := fnv(src)
		compBuf.Reset()
		zw.Reset(&compBuf)
		if _, err := zw.Write(src); err != nil {
			panic(err)
		}
		if err := zw.Close(); err != nil {
			panic(err)
		}
		rd.Reset(compBuf.Bytes())
		if err := zr.(flate.Resetter).Reset(&rd, nil); err != nil {
			panic(err)
		}
		decLen := 0
		for {
			k, err := zr.Read(decBuf[decLen:])
			decLen += k
			if err == io.EOF {
				break
			}
			if err != nil {
				panic(err)
			}
		}
		if err := zr.Close(); err != nil {
			panic(err)
		}
		if decLen != n {
			panic("length mismatch")
		}
		got := fnv(decBuf[:decLen])
		if got != want {
			panic("checksum mismatch")
		}
		total += got + uint64(decLen)
		idx := (r * 1009) % n
		src[idx] ^= 0x01
	}
	fmt.Println(total)
}
