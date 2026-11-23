// hashmap-heap, Go, idiomatic std: the task of ../README.md with a
// map[uint64]uint64 on the Go runtime heap (AES runtime hash seeded per map);
// grows from empty. Built with GOAMD64=v2 -pgo=off.
package main

import (
	"fmt"
	"os"
	"strconv"
)

func mix(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func round(n int, seed uint64) uint64 {
	m := map[uint64]uint64{}
	st := seed
	for i := 0; i < n; i++ {
		st += 0x9e3779b97f4a7c15
		m[mix(st)] = uint64(i)
	}
	var sum uint64
	st = seed
	for i := 0; i < n; i++ {
		st += 0x9e3779b97f4a7c15
		k := mix(st)
		if v, ok := m[k]; ok {
			sum += v
		}
		if _, ok := m[mix(st^0x5555555555555555)]; ok {
			sum++
		}
		if i%2 == 0 {
			delete(m, k)
		}
	}
	st = seed
	for i := 0; i < n; i++ {
		st += 0x9e3779b97f4a7c15
		if _, ok := m[mix(st)]; ok {
			sum += 3
		}
	}
	return sum + uint64(len(m))
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: hashmap-heap <n> <rounds> <seed>")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[1])
	rounds, _ := strconv.Atoi(os.Args[2])
	seed, _ := strconv.ParseUint(os.Args[3], 10, 64)
	var total uint64
	for r := 0; r < rounds; r++ {
		total += round(n, seed+uint64(r))
	}
	fmt.Println(total)
}
