// vm, Go, std: the task of README.md, a 23-opcode register-based bytecode
// virtual machine running four deterministic bytecode programs (recursive
// fibonacci, iterative prime sieve, arithmetic/checksum loop, and Shell sort
// plus weighted dot product) over a flat register file, call-frame stack and
// bounds-checked array heap. Pure safe Go (no unsafe, no //go: directives).
// All arithmetic uses int64 without overflow.
// Usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const (
	opHalt     = 0
	opLoadI    = 1
	opLoadK    = 2
	opMov      = 3
	opAdd      = 4
	opSub      = 5
	opMul      = 6
	opDiv      = 7
	opMod      = 8
	opAddI     = 9
	opBand     = 10
	opBxor     = 11
	opEq       = 12
	opLt       = 13
	opLe       = 14
	opJmp      = 15
	opJz       = 16
	opJnz      = 17
	opCall     = 18
	opRet      = 19
	opArrAlloc = 20
	opArrGet   = 21
	opArrSet   = 22

	modSum int64 = 1_000_000_007
)

type frame struct {
	retPC      int
	callerBase int
	retReg     int
}

type heapObj struct {
	off int
	len int
}

type program struct {
	code   []uint32
	consts []int64
	funcs  []int
}

type vmState struct {
	regs     []int64
	frames   []frame
	heapData []int64
	heapObjs []heapObj
}

func encABC(op, a, b, c uint32) uint32 {
	return (op & 255) | ((a & 255) << 8) | ((b & 255) << 16) | ((c & 255) << 24)
}

func encAU(op, a, u uint32) uint32 {
	return (op & 255) | ((a & 255) << 8) | ((u & 65535) << 16)
}

func encAS(op, a uint32, s int) uint32 {
	u := uint32(uint16(int16(s)))
	return (op & 255) | ((a & 255) << 8) | (u << 16)
}

func signExt16(insn uint32) int64 {
	return int64(int16(uint16(insn >> 16)))
}

func buildFib() program {
	return program{
		code: []uint32{
			encAS(opLoadI, 1, 1),
			encABC(opLe, 1, 0, 1),
			encAU(opJz, 1, 4),
			encABC(opRet, 0, 0, 0),
			encABC(opMov, 4, 0, 0),
			encAS(opAddI, 4, -1),
			encABC(opCall, 2, 4, 0),
			encABC(opMov, 4, 0, 0),
			encAS(opAddI, 4, -2),
			encABC(opCall, 3, 4, 0),
			encABC(opAdd, 0, 2, 3),
			encABC(opRet, 0, 0, 0),
		},
		funcs: []int{0},
	}
}

func buildSieve() program {
	return program{
		code: []uint32{
			encABC(opMov, 1, 0, 0),
			encAS(opAddI, 1, 1),
			encABC(opArrAlloc, 2, 1, 0),
			encAS(opLoadI, 5, 1),
			encAS(opLoadI, 3, 2),
			encABC(opMul, 6, 3, 3),
			encABC(opLe, 6, 6, 0),
			encAU(opJz, 6, 18),
			encABC(opArrGet, 6, 2, 3),
			encAU(opJnz, 6, 16),
			encABC(opMul, 4, 3, 3),
			encABC(opLe, 6, 4, 0),
			encAU(opJz, 6, 16),
			encABC(opArrSet, 2, 4, 5),
			encABC(opAdd, 4, 4, 3),
			encAU(opJmp, 0, 11),
			encAS(opAddI, 3, 1),
			encAU(opJmp, 0, 5),
			encAS(opLoadI, 7, 0),
			encAS(opLoadI, 8, 0),
			encAS(opLoadI, 3, 2),
			encABC(opLe, 6, 3, 0),
			encAU(opJz, 6, 29),
			encABC(opArrGet, 6, 2, 3),
			encAU(opJnz, 6, 27),
			encAS(opAddI, 7, 1),
			encABC(opAdd, 8, 8, 3),
			encAS(opAddI, 3, 1),
			encAU(opJmp, 0, 21),
			encAU(opLoadK, 9, 0),
			encABC(opMod, 8, 8, 9),
			encABC(opMul, 7, 7, 9),
			encABC(opAdd, 0, 7, 8),
			encABC(opHalt, 0, 0, 0),
		},
		consts: []int64{1_000_000},
	}
}

func buildArith() program {
	return program{
		code: []uint32{
			encAS(opLoadI, 2, 0),
			encAS(opLoadI, 3, 0),
			encAU(opLoadK, 4, 0),
			encAU(opLoadK, 5, 1),
			encAU(opLoadK, 6, 2),
			encAU(opLoadK, 7, 3),
			encAU(opLoadK, 8, 4),
			encABC(opLt, 9, 3, 0),
			encAU(opJz, 9, 21),
			encABC(opMul, 1, 1, 5),
			encABC(opAdd, 1, 1, 6),
			encABC(opBand, 1, 1, 7),
			encABC(opDiv, 9, 1, 8),
			encABC(opMod, 10, 1, 8),
			encABC(opSub, 11, 1, 10),
			encABC(opBxor, 11, 11, 9),
			encABC(opEq, 9, 10, 8),
			encABC(opAdd, 2, 2, 11),
			encABC(opMod, 2, 2, 4),
			encAS(opAddI, 3, 1),
			encAU(opJmp, 0, 7),
			encABC(opHalt, 2, 0, 0),
		},
		consts: []int64{1_000_000_007, 1103515245, 12345, 0x7fffffff, 97},
	}
}

func buildSortDot() program {
	return program{
		code: []uint32{
			encABC(opArrAlloc, 2, 0, 0),
			encABC(opArrAlloc, 3, 0, 0),
			encAU(opLoadK, 15, 0),
			encAU(opLoadK, 12, 1),
			encAU(opLoadK, 13, 2),
			encAU(opLoadK, 14, 3),
			encAS(opLoadI, 11, 2),
			encAS(opLoadI, 4, 0),
			encABC(opLt, 9, 4, 0),
			encAU(opJz, 9, 19),
			encABC(opMul, 1, 1, 13),
			encAS(opAddI, 1, 17),
			encABC(opMod, 1, 1, 12),
			encABC(opArrSet, 2, 4, 1),
			encABC(opMod, 10, 4, 14),
			encAS(opAddI, 10, 1),
			encABC(opArrSet, 3, 4, 10),
			encAS(opAddI, 4, 1),
			encAU(opJmp, 0, 8),
			encABC(opDiv, 6, 0, 11),
			encAU(opJz, 6, 40),
			encABC(opMov, 4, 6, 0),
			encABC(opLt, 9, 4, 0),
			encAU(opJz, 9, 38),
			encABC(opArrGet, 7, 2, 4),
			encABC(opMov, 5, 4, 0),
			encABC(opLe, 9, 6, 5),
			encAU(opJz, 9, 35),
			encABC(opSub, 10, 5, 6),
			encABC(opArrGet, 8, 2, 10),
			encABC(opLt, 9, 7, 8),
			encAU(opJz, 9, 35),
			encABC(opArrSet, 2, 5, 8),
			encABC(opMov, 5, 10, 0),
			encAU(opJmp, 0, 26),
			encABC(opArrSet, 2, 5, 7),
			encAS(opAddI, 4, 1),
			encAU(opJmp, 0, 22),
			encABC(opDiv, 6, 6, 11),
			encAU(opJmp, 0, 20),
			encAS(opLoadI, 8, 0),
			encAS(opLoadI, 4, 0),
			encABC(opLt, 9, 4, 0),
			encAU(opJz, 9, 51),
			encABC(opArrGet, 7, 2, 4),
			encABC(opArrGet, 10, 3, 4),
			encABC(opMul, 7, 7, 10),
			encABC(opAdd, 8, 8, 7),
			encABC(opMod, 8, 8, 15),
			encAS(opAddI, 4, 1),
			encAU(opJmp, 0, 42),
			encABC(opHalt, 8, 0, 0),
		},
		consts: []int64{1_000_000_007, 100003, 48271, 97},
	}
}

func exec(vm *vmState, prog *program, arg0, arg1 int64) int64 {
	vm.heapData = vm.heapData[:0]
	vm.heapObjs = vm.heapObjs[:0]
	regs := vm.regs
	frames := vm.frames
	code := prog.code
	consts := prog.consts
	funcs := prog.funcs

	regs[0] = arg0
	regs[1] = arg1
	pc := 0
	base := 0
	sp := 0

	for {
		insn := code[pc]
		pc++
		op := insn & 255
		a := int((insn >> 8) & 255)
		switch op {
		case opHalt:
			return regs[base+a]
		case opLoadI:
			regs[base+a] = signExt16(insn)
		case opLoadK:
			regs[base+a] = consts[insn>>16]
		case opMov:
			b := int((insn >> 16) & 255)
			regs[base+a] = regs[base+b]
		case opAdd:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			regs[base+a] = regs[base+b] + regs[base+c]
		case opSub:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			regs[base+a] = regs[base+b] - regs[base+c]
		case opMul:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			regs[base+a] = regs[base+b] * regs[base+c]
		case opDiv:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			regs[base+a] = regs[base+b] / regs[base+c]
		case opMod:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			regs[base+a] = regs[base+b] % regs[base+c]
		case opAddI:
			regs[base+a] += signExt16(insn)
		case opBand:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			regs[base+a] = regs[base+b] & regs[base+c]
		case opBxor:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			regs[base+a] = regs[base+b] ^ regs[base+c]
		case opEq:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			if regs[base+b] == regs[base+c] {
				regs[base+a] = 1
			} else {
				regs[base+a] = 0
			}
		case opLt:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			if regs[base+b] < regs[base+c] {
				regs[base+a] = 1
			} else {
				regs[base+a] = 0
			}
		case opLe:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			if regs[base+b] <= regs[base+c] {
				regs[base+a] = 1
			} else {
				regs[base+a] = 0
			}
		case opJmp:
			pc = int(insn >> 16)
		case opJz:
			if regs[base+a] == 0 {
				pc = int(insn >> 16)
			}
		case opJnz:
			if regs[base+a] != 0 {
				pc = int(insn >> 16)
			}
		case opCall:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			frames[sp] = frame{retPC: pc, callerBase: base, retReg: a}
			sp++
			base += b
			pc = funcs[c]
		case opRet:
			val := regs[base+a]
			if sp == 0 {
				return val
			}
			sp--
			f := frames[sp]
			base = f.callerBase
			pc = f.retPC
			regs[base+f.retReg] = val
		case opArrAlloc:
			b := int((insn >> 16) & 255)
			n := int(regs[base+b])
			if n < 0 {
				fmt.Fprintln(os.Stderr, "vm: negative array size")
				os.Exit(1)
			}
			off := len(vm.heapData)
			need := off + n
			if cap(vm.heapData) < need {
				nc := max(cap(vm.heapData)*2, need)
				nb := make([]int64, need, nc)
				copy(nb, vm.heapData)
				vm.heapData = nb
			} else {
				vm.heapData = vm.heapData[:need]
				clear(vm.heapData[off:need])
			}
			id := len(vm.heapObjs)
			vm.heapObjs = append(vm.heapObjs, heapObj{off: off, len: n})
			regs[base+a] = int64(id)
		case opArrGet:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			id := int(regs[base+b])
			idx := int(regs[base+c])
			obj := vm.heapObjs[id]
			if idx < 0 || idx >= obj.len {
				fmt.Fprintln(os.Stderr, "vm: array index out of bounds")
				os.Exit(1)
			}
			regs[base+a] = vm.heapData[obj.off+idx]
		case opArrSet:
			b := int((insn >> 16) & 255)
			c := int((insn >> 24) & 255)
			id := int(regs[base+a])
			idx := int(regs[base+b])
			obj := vm.heapObjs[id]
			if idx < 0 || idx >= obj.len {
				fmt.Fprintln(os.Stderr, "vm: array index out of bounds")
				os.Exit(1)
			}
			vm.heapData[obj.off+idx] = regs[base+c]
		default:
			fmt.Fprintf(os.Stderr, "vm: illegal opcode %d\n", op)
			os.Exit(1)
		}
	}
}

func main() {
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>")
		os.Exit(2)
	}
	parse := func(s string) int64 {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "vm:", err)
			os.Exit(2)
		}
		return v
	}
	fibN := parse(os.Args[1])
	sieveN := parse(os.Args[2])
	loopN := parse(os.Args[3])
	sortN := parse(os.Args[4])
	rounds := parse(os.Args[5])

	pFib := buildFib()
	pSieve := buildSieve()
	pArith := buildArith()
	pSort := buildSortDot()

	vm := &vmState{
		regs:   make([]int64, 65536),
		frames: make([]frame, 4096),
	}

	var sFib, sSieve, sArith, sSort int64
	for r := int64(0); r < rounds; r++ {
		seed := r + 1
		sFib = (sFib + exec(vm, &pFib, fibN, 0)) % modSum
		sSieve = (sSieve + exec(vm, &pSieve, sieveN, 0)) % modSum
		sArith = (sArith + exec(vm, &pArith, loopN, seed)) % modSum
		sSort = (sSort + exec(vm, &pSort, sortN, seed)) % modSum
	}
	total := (sFib + sSieve + sArith + sSort) % modSum
	fmt.Printf("fib=%d sieve=%d arith=%d sortdot=%d total=%d\n", sFib, sSieve, sArith, sSort, total)
}
