// hashmap-str, Go, idiomatic std: the task of ../README.md with
// map[string]uint64 (Go's Swiss-table map and its runtime hash, AES-based
// where the CPU has AES-NI and seeded per map); keys are 8-24 byte ASCII
// strings sliced from one contiguous string buffer, and the map grows
// from empty each round. Built with GOAMD64=v2 -pgo=off.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const alph = "abcdefghijklmnopqrstuvwxyz012345"

func mix(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

type span struct {
	lo, hi int
}

func fillKey(buf []byte, idx int, tag byte, st *uint64, seed uint64) ([]byte, span) {
	lo := len(buf)
	*st += 0x9e3779b97f4a7c15
	z := mix(*st)
	length := 8 + int(z%17)
	u := (uint64(idx) * 0x9e3779b97f4a7c15) ^ (seed * 0x517cc1b727220a95)
	for j := 0; j < 7; j++ {
		buf = append(buf, alph[(u>>(5*j))&31])
	}
	buf = append(buf, tag)
	w := z >> 8
	for j := 8; j < length; j++ {
		if j == 16 {
			*st += 0x9e3779b97f4a7c15
			w = mix(*st)
		}
		buf = append(buf, alph[w&31])
		w >>= 5
	}
	return buf, span{lo, len(buf)}
}

func round(keys, misses []string, r uint64) uint64 {
	m := map[string]uint64{}
	for i, k := range keys {
		m[k] = uint64(i) + r
	}
	var sum uint64
	for i, k := range keys {
		if v, ok := m[k]; ok {
			sum += v
		}
		if _, ok := m[misses[i]]; ok {
			sum++
		}
		if i%2 == 0 {
			delete(m, k)
		}
	}
	for _, k := range keys {
		if _, ok := m[k]; ok {
			sum += 3
		}
	}
	return sum + uint64(len(m))
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: hashmap-str <n> <rounds> <seed>")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[1])
	rounds, _ := strconv.Atoi(os.Args[2])
	seed, _ := strconv.ParseUint(os.Args[3], 10, 64)
	buf := make([]byte, 0, 2*n*24)
	kspans := make([]span, n)
	mspans := make([]span, n)
	st := seed
	for i := 0; i < n; i++ {
		buf, kspans[i] = fillKey(buf, i, 'k', &st, seed)
	}
	for i := 0; i < n; i++ {
		buf, mspans[i] = fillKey(buf, i, 'm', &st, seed)
	}
	text := string(buf)
	keys := make([]string, n)
	misses := make([]string, n)
	for i, sp := range kspans {
		keys[i] = text[sp.lo:sp.hi]
	}
	for i, sp := range mspans {
		misses[i] = text[sp.lo:sp.hi]
	}
	var total uint64
	for r := 0; r < rounds; r++ {
		total += round(keys, misses, uint64(r))
	}
	fmt.Println(total)
}
