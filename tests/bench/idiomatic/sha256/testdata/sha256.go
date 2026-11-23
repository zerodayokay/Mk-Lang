// sha256, Go, idiomatic std: the task of ../README.md with crypto/sha256
// over a generated multi-MiB SplitMix64 buffer: bulk_reps full-buffer passes
// plus short_reps passes over 64-byte slices, XOR-folding each digest's first
// 8 little-endian bytes and mutating one buffer byte after each pass. Built
// with GOAMD64=v2 -pgo=off.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
)

func mix(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func lo64(d [32]byte) uint64 {
	return binary.LittleEndian.Uint64(d[:8])
}

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: sha256 <mib> <bulk_reps> <short_reps> <seed>")
		os.Exit(2)
	}
	mib, _ := strconv.Atoi(os.Args[1])
	bulkReps, _ := strconv.Atoi(os.Args[2])
	shortReps, _ := strconv.Atoi(os.Args[3])
	st, _ := strconv.ParseUint(os.Args[4], 10, 64)
	n := mib * 1048576
	buf := make([]byte, n)
	for i := 0; i < n; i += 8 {
		st += 0x9e3779b97f4a7c15
		binary.LittleEndian.PutUint64(buf[i:], mix(st))
	}
	var sum uint64
	for r := 0; r < bulkReps; r++ {
		h := lo64(sha256.Sum256(buf))
		sum ^= h
		buf[r%n] ^= byte(h)
	}
	for r := 0; r < shortReps; r++ {
		for j := 0; j < n; j += 64 {
			sum ^= lo64(sha256.Sum256(buf[j : j+64]))
		}
		buf[(r*997)%n] ^= byte(sum)
	}
	fmt.Println(sum)
}
