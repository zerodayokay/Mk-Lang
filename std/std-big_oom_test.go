// Why: every allocating std.big operation must report OutOfMemory cleanly,
// never panic, and leak nothing on the error path.
// What: TestSTDBIG_OutOfMemory.
// How: every allocating operation runs over an arena on a
// heap that fails the n-th request, then repeats with a heap that fails
// every request from n onward, for each n until no failure is reached. This
// covers recovery from a single failure and propagation when allocation
// fallbacks are exhausted. At each n the operation either returns its result,
// equal to the run with no failure (the arena grew past the failure in a
// chunk of its own), or fails with OutOfMemory, never anything else and
// never a panic; and after the arena is freed no byte is live, so the
// sub-arenas of Text, Parse and the recursive division are freed on the
// error path too. Operands are long enough (up to 2,400 words) that each
// operation asks the heap for several chunks.
//
// The failing heap needs std.core's MakeHeap, so it lives in a fixture
// module std.bigfix of the test's std copy, listed in the copy's
// unsafe_modules (the pattern of std-coll_growoom_test.go).

package std

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const bigOomFixture = `// Fixture of the std.big OOM test: a heap that fails one or every request
// starting at the fail_at-th alloc or growing resize (counted from 0; none
// when fail_at is negative), and a driver that runs one std.big operation over an arena
// on it.

import std.core
import std.raw
import std.big

struct Fail {
    inner: *Alloc
    live: int
    reqs: int
    fail_at: int
    exhaust: bool
    fails: int
}

const FailTable = core.AllocTable(alloc: FailAlloc, resize: FailResize, free: FailFree)

FailAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        var c := raw.Read(q)
        c.reqs += 1
        var p := raw.Null[u8]()
        if c.fail_at >= 0 && (c.reqs - 1 == c.fail_at || (c.exhaust && c.reqs - 1 > c.fail_at)) {
            c.fails += 1
        } else {
            p = core.AllocBytes(&*c.inner, size, align)
            if !raw.IsNull(p) {
                c.live += size
            }
        }
        raw.Write(q, c)
        return p
    }
}

FailResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        var c := raw.Read(q)
        var ok := false
        var refused := false
        if new > old {
            c.reqs += 1
            if c.fail_at >= 0 && (c.reqs - 1 == c.fail_at || (c.exhaust && c.reqs - 1 > c.fail_at)) {
                c.fails += 1
                refused = true
            }
        }
        if !refused {
            ok = core.ResizeBytes(&*c.inner, p, old, new, align)
            if ok {
                c.live += new - old
            }
        }
        raw.Write(q, c)
        return ok
    }
}

FailFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        var c := raw.Read(q)
        core.FreeBytes(&*c.inner, p, size, align)
        c.live -= size
        raw.Write(q, c)
    }
}

Live(c: &Fail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).live
    }
}

Fails(c: &Fail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).fails
    }
}

Fnv(h: u64, x: u64): u64 {
    return (h ^ x) *% 1099511628211
}

Digest(x: &big.Int): u64 {
    var h: u64 = 14695981039346656037
    if x.Sign() < 0 {
        h = Fnv(h, 1)
    }
    for w in x.Words() {
        h = Fnv(h, w)
    }
    return h
}

DigestBytes(b: []u8): u64 {
    var h: u64 = 14695981039346656037
    for c in b {
        h = Fnv(h, u64(c))
    }
    return h
}

// Op runs operation op in ar and returns its result's digest.
Op(ar: &Arena, op: int, x: &big.Int, y: &big.Int, m: &big.Int, s: str): big.Error!u64 {
    if op == 0 {
        return Digest(&(try x.Add(ar, y)))
    } else if op == 1 {
        return Digest(&(try x.Sub(ar, y)))
    } else if op == 2 {
        return Digest(&(try x.Mul(ar, y)))
    } else if op == 3 {
        (q, r) := try x.DivMod(ar, y)
        return Fnv(Digest(&q), Digest(&r))
    } else if op == 4 {
        return Digest(&(try x.Mod(ar, y)))
    } else if op == 5 {
        return Digest(&(try y.ModPow(ar, m, m)))
    } else if op == 6 {
        // An even modulus: the Algorithm D reduction path.
        e := try m.Shl(ar, 1)
        return Digest(&(try y.ModPow(ar, m, &e)))
    } else if op == 7 {
        match try y.ModInverse(ar, m) {
            some(v) {
                return Digest(&v)
            }
            none {
                return 1
            }
        }
    } else if op == 8 {
        return Digest(&(try x.Gcd(ar, y)))
    } else if op == 9 {
        return Digest(&(try x.Sqrt(ar)))
    } else if op == 10 {
        return Digest(&(try x.Shl(ar, 777)))
    } else if op == 11 {
        return Digest(&(try x.Shr(ar, 777)))
    } else if op == 12 {
        return Digest(&(try x.Neg(ar)))
    } else if op == 13 {
        return Digest(&(try x.Clone(ar)))
    } else if op == 14 {
        return DigestBytes(try x.Bytes(ar))
    } else if op == 15 {
        return DigestBytes((try x.Text(ar, 10)).AsBytes())
    } else if op == 16 {
        return DigestBytes((try x.Text(ar, 16)).AsBytes())
    } else if op == 17 {
        return DigestBytes((try y.Text(ar, 7)).AsBytes())
    } else if op == 18 {
        return Digest(&(try big.Parse(ar, s, 10, 1000000)))
    } else if op == 19 {
        return Digest(&(try big.FromBytes(ar, s.AsBytes())))
    } else if op == 20 {
        return Digest(&(try big.FromI64(ar, -12345)))
    }
    return 0
}

// Run runs op once, prints the fault index, policy, result and live bytes,
// and returns the failures met.
export Run(w: mut Writer, a: &Alloc, fail_at: int, exhaust: bool, op: int, x: &big.Int, y: &big.Int, m: &big.Int, s: str): WriteError!int {
    var c := Fail(inner: raw.Of(a), live: 0, reqs: 0, fail_at: fail_at, exhaust: exhaust, fails: 0)
    unsafe "c outlives the heap made over it, freed below" {
        h := core.MakeHeap(raw.PtrCast[Fail, u8](raw.Of(&c)), raw.Of(&FailTable))
        var ar := Arena.New(&h) else {
            f := Fails(&c)
            live := Live(&c)
            try Print(w, "{op} {fail_at} {exhaust} {f} new 0 {live}\n", (op, fail_at, exhaust, f, live))
            return f
        }
        var res := "ok"
        var d: u64 = 0
        match Op(&ar, op, x, y, m, s) {
            ok(v) {
                d = v
            }
            err(e) {
                match e {
                    OutOfMemory {
                        res = "OutOfMemory"
                    }
                    _ {
                        res = "other"
                    }
                }
            }
        }
        ar.Free()
        f := Fails(&c)
        live := Live(&c)
        try Print(w, "{op} {fail_at} {exhaust} {f} {res} {d} {live}\n", (op, fail_at, exhaust, f, res, d, live))
        return f
    }
}
`

const bigOomProgram = `import std.big
import std.bigfix

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

Rand(a: &Arena, n: int, seed: u64): OutOfMemory!big.Int from a {
    z := try a.Slice[u64](n)
    for i in 0..n {
        z[i] = Mix(seed +% Trunc[u64](i))
    }
    z[0] = z[0] | 1
    z[n - 1] = z[n - 1] | 1
    return big.FromWords(z[..], false)
}

export Main(sys: &System): WriteError|OutOfMemory!void {
    x := try Rand(sys.arena, 2400, 1)
    y := try Rand(sys.arena, 1100, 2)
    m := try Rand(sys.arena, 24, 3)
    s := try x.Text(sys.arena, 10)
    var out := sys.Stdout()
    for op in 0..21 {
        _ = try bigfix.Run(mut out, sys.heap.Alloc(), -1, false, op, &x, &y, &m, s)
        for exhaust in [false, true] {
            var n := 0
            for {
                f := try bigfix.Run(mut out, sys.heap.Alloc(), n, exhaust, op, &x, &y, &m, s)
                if f == 0 {
                    break
                }
                n += 1
            }
        }
    }
}
`

var bigOomLine = regexp.MustCompile(`^(\d+) (-?\d+) (true|false) (\d+) (ok|OutOfMemory|other|new) (\d+) (-?\d+)$`)

// TestSTDBIG_OutOfMemory: see the file comment.
func TestSTDBIG_OutOfMemory(t *testing.T) {
	for _, flags := range bigModes {
		name := "dev"
		if len(flags) > 0 {
			name = "release"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			stdDir := bigStdDir(t, root)
			manifest, err := os.ReadFile(filepath.Join(stdDir, "pkg.mk"))
			if err != nil {
				t.Fatal(err)
			}
			m := string(manifest)
			if !strings.Contains(m, `unsafe_modules: [`) {
				t.Fatalf("pkg.mk: no unsafe_modules list to extend:\n%s", m)
			}
			m = strings.Replace(m, `unsafe_modules: [`, `unsafe_modules: ["bigfix", `, 1)
			for p, text := range map[string]string{"pkg.mk": m, "bigfix/bigfix.mk": bigOomFixture} {
				dst := filepath.Join(stdDir, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(bigOomProgram), 0o644); err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(root, "main.exe")
			args := append([]string{"build"}, flags...)
			args = append(args, "-o", exe, "main.mk")
			out, status := mkCmd(t, root, stdDir, args...)
			if status != 0 {
				if strings.Contains(out, "no usable LLVM") {
					t.Skipf("needs the LLVM toolchain: %s", out)
				}
				t.Fatalf("mk build %v: exit %d\n%s", flags, status, out)
			}
			r := runNative(t, exe, "")
			if r.code != 0 {
				t.Fatalf("driver exit %d\n%s", r.code, r.stderr)
			}
			ref := map[string]string{}
			runs := map[string]int{}
			ooms := map[string]int{}
			for _, line := range strings.Split(strings.TrimSpace(strings.ReplaceAll(r.stdout, "\r\n", "\n")), "\n") {
				g := bigOomLine.FindStringSubmatch(line)
				if g == nil {
					t.Fatalf("bad line %q", line)
				}
				op, failAt, exhaust, fails, res, digest, live := g[1], g[2], g[3], g[4], g[5], g[6], g[7]
				if live != "0" {
					t.Errorf("%s: %s bytes still live", line, live)
				}
				if res == "new" {
					if failAt != "0" || fails == "0" {
						t.Errorf("%s: Arena.New failed outside the first request", line)
					}
					continue
				}
				runs[op]++
				switch {
				case failAt == "-1":
					if fails != "0" || res != "ok" {
						t.Errorf("%s: the reference run failed", line)
					}
					ref[op] = digest
				case res == "ok":
					if digest != ref[op] {
						t.Errorf("%s: result differs from the reference %s", line, ref[op])
					}
				case res == "OutOfMemory":
					if fails == "0" {
						t.Errorf("%s: OutOfMemory with no failure met", line)
					}
					if exhaust == "true" {
						ooms[op]++
					}
				default:
					t.Errorf("%s: an error other than OutOfMemory", line)
				}
			}
			// FromI64 (op 20) needs one word, which the arena's first
			// chunk holds, so only Arena.New can fail under it.
			for op := 0; op < 20; op++ {
				k := strconv.Itoa(op)
				if ooms[k] < 1 {
					t.Errorf("op %d: %d runs, none failed with OutOfMemory when requests were exhausted", op, runs[k])
				}
			}
			t.Logf("runs %v, exhaustion runs with OutOfMemory %v", runs, ooms)
		})
	}
}
