// Why: a C import's struct of many fields is started zeroed with
// mem.Zeroed, as C's `= {0}` does; every shape a binding gives a struct's
// fields must be zero-valid, or the call would be E0701.
// What: TestCImport_ZeroedStruct.
// How: cBin's package over a header whose struct has every field shape a
// binding emits; the bin zeroes it outside unsafe, C reads it back, and
// the bin sets two fields and asks again.

package driver

import "testing"

// TestCImport_ZeroedStruct: mem.Zeroed of a 30-field C struct (integers of
// each width, float, double, _Bool, pointers, a function pointer, an array,
// a nested struct by value and an array of them, a union, bitfields) builds
// in safe code, and C reads every field as zero.
func TestCImport_ZeroedStruct(t *testing.T) {
	needTools(t)
	header := `#include <stdint.h>
#include <stdbool.h>
typedef struct inner { int32_t a; double b; } inner;
typedef union num { int64_t i; double d; uint8_t bytes[8]; } num;
typedef int32_t (*k_cb)(int32_t);
typedef struct big {
    int8_t f01; int16_t f02; int32_t f03; int64_t f04; uint8_t f05; uint16_t f06; uint32_t f07;
    uint64_t f08; float f09; double f10; bool f11; char f12; void *f13; const char *f14;
    int32_t f15[4]; int32_t f16; int32_t f17; int32_t f18; int32_t f19; int32_t f20;
    unsigned bits_a : 3; unsigned bits_b : 5; inner in; inner ins[2]; num n; k_cb cb;
    int32_t f27; int32_t f28; int32_t f29; int32_t f30;
} big;
int32_t k_nonzero(const big *b);
`
	c := `#include "lib/k.h"
int32_t k_nonzero(const big *b) {
    int32_t n = 0;
    n += b->f01 != 0; n += b->f02 != 0; n += b->f03 != 0; n += b->f04 != 0; n += b->f05 != 0;
    n += b->f06 != 0; n += b->f07 != 0; n += b->f08 != 0; n += b->f09 != 0; n += b->f10 != 0;
    n += b->f11 != 0; n += b->f12 != 0; n += b->f13 != 0; n += b->f14 != 0;
    for (int i = 0; i < 4; i++) n += b->f15[i] != 0;
    n += b->f16 != 0; n += b->f17 != 0; n += b->f18 != 0; n += b->f19 != 0; n += b->f20 != 0;
    n += b->bits_a != 0; n += b->bits_b != 0; n += b->in.a != 0; n += b->in.b != 0;
    for (int i = 0; i < 2; i++) n += (b->ins[i].a != 0) + (b->ins[i].b != 0);
    n += b->n.i != 0; n += b->cb != 0;
    n += b->f27 != 0; n += b->f28 != 0; n += b->f29 != 0; n += b->f30 != 0;
    return n;
}
`
	main := `import c.k
import std.mem
import std.raw

export Main(sys: &System): WriteError!void {
    var b := mem.Zeroed[k.Big]() // safe: every field of a C struct is zero-valid
    var z: i32 = -1
    unsafe "k_nonzero reads b" {
        z = k.k_nonzero(raw.Of(&b))
    }
    b.f30 = 1
    b.in_.a = 2
    var n: i32 = -1
    unsafe "k_nonzero reads b" {
        n = k.k_nonzero(raw.Of(&b))
    }
    try sys.Print("{z} {n}\n", (z, n))
}
`
	dir := writeTree(t, cBin("zs", header, c, main))
	if out := buildRun(t, dir, t.TempDir()); out != "0 2\n" {
		t.Errorf("printed %q, want 0 2", out)
	}
}
