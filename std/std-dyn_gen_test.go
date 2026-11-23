// Why: gen dyn.Of(I) (std/dyn/dyn.mk, ref.mk, erased/erased.mk) writes the
// dynamic form of an interface, and its output and refusals are public.
// What: the golden output, the E0903 refusals, the boxed type as a view,
// the kernel example of doc/examples.md in hosted form, and a warm cache.
// How: mk check over the real std, each command with its own cache
// (dynRoot), so tests run in parallel; native runs are std-dyn_native_test.go.

package std

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dynRoot is one scratch directory with a copy of std and a cache of its
// own beside the work directory dir, where the program or package goes (a
// package root must not hold the std copy, or its directories would be
// the package's modules).
type dynRoot struct {
	dir, std, cache string
}

func newDynRoot(t *testing.T, fixtures map[string]string) *dynRoot {
	t.Helper()
	root := t.TempDir()
	return &dynRoot{dir: filepath.Join(root, "w"), std: copyStd(t, root, fixtures), cache: filepath.Join(root, "cache")}
}

func (r *dynRoot) write(t *testing.T, rel, text string) {
	t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mk runs mk with args in the root, MK_STD and MK_CACHE set, and returns
// its combined output and exit status.
func (r *dynRoot) mk(t *testing.T, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), mkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, mkExe(t), args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "MK_STD="+r.std, "MK_CACHE="+r.cache)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("mk %s: killed after %s\n%s", strings.Join(args, " "), mkTimeout, out.String())
	}
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(out.String(), "\r\n", "\n"), status
}

// dynCheck runs mk check on src as main.mk over a copy of std.
func dynCheck(t *testing.T, src string) (string, int) {
	t.Helper()
	r := newDynRoot(t, nil)
	r.write(t, "main.mk", src)
	return r.mk(t, "check", "main.mk")
}

// dynPkg is a package whose one bin's module, app, is src.
const dynPkg = `const Package = pkg.Package(
    name: "p",
    bins: [pkg.Bin(name: "app", root: "app", link_script: "", cpu: "", subsystem: "console")],
    deps: [],
    c_sources: [],
    c_imports: [],
    asm_sources: [],
    targets: [],
    unsafe_modules: [],
    links: [],
)
`

// dynView checks src as module app of a package and returns mk's output, its
// status, and the gen view (app/gen/x86_64-windows/gen.mk) without its
// header, trace and hash comment lines.
func dynView(t *testing.T, src string) (string, int, string) {
	t.Helper()
	r := newDynRoot(t, nil)
	r.write(t, "pkg.mk", dynPkg)
	r.write(t, "app/app.mk", src)
	out, status := r.mk(t, "check", "--target=x86_64-windows", ".")
	b, err := os.ReadFile(filepath.Join(r.dir, "app", "gen", "x86_64-windows", "gen.mk"))
	if err != nil {
		t.Fatalf("no gen view: %v\nmk check: exit %d\n%s", err, status, out)
	}
	var keep []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(l, "//") {
			keep = append(keep, l)
		}
	}
	return out, status, strings.TrimSpace(strings.Join(keep, "\n")) + "\n"
}

// kernelDevice is the interface and gen line of doc/examples.md's
// kernel, verbatim.
const kernelDevice = `import std.dyn

interface Device {
    Write(mut self, data: []u8): int
}

gen dyn.Of(Device)
`

// kernelGolden is what gen dyn.Of(Device) emits (the view struct, with the
// fn fields held in the record table).
const kernelGolden = `mut view struct DeviceDyn {
    obj: dyn.Ref
    table: (write: fn(mut dyn.Ref, []u8): int)
}

DeviceDyn.Of[T: Device](x: mut T): DeviceDyn from x {
    return DeviceDyn(obj: dyn.Ref.Of(x), table: (write: DeviceDyn.AdaptWrite[T]))
}

DeviceDyn.AdaptWrite[T: Device](obj: mut dyn.Ref, data: []u8): int {
    return obj.Mut[T]().Write(data)
}

impl Device for DeviceDyn {
    Write(mut self, data: []u8): int {
        f := self.table.write
        return f(mut self.obj, data)
    }
}
`

// storeIface covers every receiver and result form dyn.Of forwards (and,
// with more than one method, one table slot per line): &self
// and mut self, ?T, E!void with a two-error set, E!T, void, several
// parameters, and names whose snake_case has an acronym (ReadAt, HTTPGet,
// ID). Store is exported, so every generated item is.
const storeIface = `import std.dyn

export error Closed
export error Full(n: int)

export interface Store {
    Get(&self, key: str): ?int
    Put(mut self, key: str, value: int): Closed|Full!void
    Take(mut self, n: int, flags: u8): Closed!int
    Reset(mut self)
    ReadAt(&self, at: []u8, n: int): int
    HTTPGet(&self): str
    ID(&self): u64
}

gen dyn.Of(Store)
`

const storeGolden = `export mut view struct StoreDyn {
    obj: dyn.Ref
    table: (
        get: fn(&dyn.Ref, str): ?int,
        put: fn(mut dyn.Ref, str, int): Closed|Full!void,
        take: fn(mut dyn.Ref, int, u8): Closed!int,
        reset: fn(mut dyn.Ref),
        read_at: fn(&dyn.Ref, []u8, int): int,
        http_get: fn(&dyn.Ref): str,
        id: fn(&dyn.Ref): u64
    )
}

export StoreDyn.Of[T: Store](x: mut T): StoreDyn from x {
    return StoreDyn(obj: dyn.Ref.Of(x), table: (
        get: StoreDyn.AdaptGet[T],
        put: StoreDyn.AdaptPut[T],
        take: StoreDyn.AdaptTake[T],
        reset: StoreDyn.AdaptReset[T],
        read_at: StoreDyn.AdaptReadAt[T],
        http_get: StoreDyn.AdaptHTTPGet[T],
        id: StoreDyn.AdaptID[T]
    ))
}

StoreDyn.AdaptGet[T: Store](obj: &dyn.Ref, key: str): ?int {
    return obj.Get[T]().Get(key)
}

StoreDyn.AdaptPut[T: Store](obj: mut dyn.Ref, key: str, value: int): Closed|Full!void {
    try obj.Mut[T]().Put(key, value)
}

StoreDyn.AdaptTake[T: Store](obj: mut dyn.Ref, n: int, flags: u8): Closed!int {
    return try obj.Mut[T]().Take(n, flags)
}

StoreDyn.AdaptReset[T: Store](obj: mut dyn.Ref) {
    obj.Mut[T]().Reset()
}

StoreDyn.AdaptReadAt[T: Store](obj: &dyn.Ref, at: []u8, n: int): int {
    return obj.Get[T]().ReadAt(at, n)
}

StoreDyn.AdaptHTTPGet[T: Store](obj: &dyn.Ref): str {
    return obj.Get[T]().HTTPGet()
}

StoreDyn.AdaptID[T: Store](obj: &dyn.Ref): u64 {
    return obj.Get[T]().ID()
}

impl Store for StoreDyn {
    Get(&self, key: str): ?int {
        f := self.table.get
        return f(&self.obj, key)
    }
    Put(mut self, key: str, value: int): Closed|Full!void {
        f := self.table.put
        try f(mut self.obj, key, value)
    }
    Take(mut self, n: int, flags: u8): Closed!int {
        f := self.table.take
        return try f(mut self.obj, n, flags)
    }
    Reset(mut self) {
        f := self.table.reset
        f(mut self.obj)
    }
    ReadAt(&self, at: []u8, n: int): int {
        f := self.table.read_at
        return f(&self.obj, at, n)
    }
    HTTPGet(&self): str {
        f := self.table.http_get
        return f(&self.obj)
    }
    ID(&self): u64 {
        f := self.table.id
        return f(&self.obj)
    }
}
`

// TestSTDDYN_Golden: gen dyn.Of over the kernel's Device and over Store
// prints exactly the golden text, and the output checks (stage 5) with no
// diagnostic: the view struct, Of over the adapters named as instance
// values, one adapter per method, and impl I for IDyn.
func TestSTDDYN_Golden(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, src, want string }{
		{"Device", kernelDevice + "\nexport Main(sys: &System) {\n}\n", kernelGolden},
		{"Store", storeIface + "\nexport Main(sys: &System) {\n}\n", storeGolden},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, status, view := dynView(t, c.src)
			if status != 0 {
				t.Fatalf("mk check: exit %d\n%s", status, out)
			}
			if view != c.want {
				t.Errorf("gen view:\n%s\nwant:\n%s", view, c.want)
			}
		})
	}
}

// uartDecl is a hosted stand-in for the kernel's Uart: it writes into a
// fixed buffer instead of a mapped register, so a test can read it back.
const uartDecl = `struct Uart {
    buf: [64]u8
    n: int
}

impl Device for Uart {
    Write(mut self, data: []u8): int {
        for b in data {
            if self.n < 64 {
                self.buf[self.n] = b
                self.n += 1
            }
        }
        return data.Len()
    }
}

struct Null {
    dropped: int
}

impl Device for Null {
    Write(mut self, data: []u8): int {
        self.dropped += data.Len()
        return 0
    }
}
`

// kernelHosted is doc/examples.md's kernel in hosted form: Device and its
// gen line verbatim, a Kernel struct holding a DeviceDyn, a syscall that
// writes through it, and the boxing in the entry with T inferred. The frame
// allocator and the trap entry are left out (they need std.free and naked
// functions), and so is a global `var State: atomic.Spin[?Kernel]`, which
// does not check: a DeviceDyn is not Share (E1002, its Ref's *u8).
// TestDyn_KernelGlobalNotShare pins that.
const kernelHosted = kernelDevice + "\n" + uartDecl + `
struct Kernel {
    console: DeviceDyn
    writes: int
}

Syscall(k: mut Kernel, num: u64, data: []u8): u64 {
    match num {
        1 {
            k.writes += 1
            return Cast[u64](k.console.Write(data))
        }
        _ {
            return Trunc[u64](-1)
        }
    }
}

Boot(uart: mut Uart): u64 {
    console := DeviceDyn.Of(uart)
    var k := Kernel(console: console, writes: 0)
    a := Syscall(mut k, 1, "hello".AsBytes())
    b := Syscall(mut k, 1, " kernel".AsBytes())
    if Syscall(mut k, 9, "".AsBytes()) != Trunc[u64](-1) {
        return 0
    }
    return a + b + Cast[u64](k.writes)
}
`

// TestSTDDYN_KernelChecks: the hosted kernel, two implementations behind
// one array of DeviceDyn, and a DeviceDyn passed where T: Device is wanted
// (impl Device for DeviceDyn) check with no diagnostic.
func TestSTDDYN_KernelChecks(t *testing.T) {
	t.Parallel()
	src := kernelHosted + `
Fan(u: mut Uart, z: mut Null): int {
    var ds := [DeviceDyn.Of(u), DeviceDyn.Of(z)]
    var total := 0
    for d in mut ds {
        total += d.Write("ab".AsBytes())
    }
    return total
}

Drive[T: Device](d: mut T): int {
    return d.Write("x".AsBytes())
}

Twice(u: mut Uart): int {
    var d := DeviceDyn.Of(u)
    return Drive(mut d)
}

export Main(sys: &System) {
    var u := Uart(buf: [0; 64], n: 0)
    var z := Null(dropped: 0)
    _ = Boot(mut u)
    _ = Fan(mut u, mut z)
    _ = Twice(mut u)
}
`
	if out, status := dynCheck(t, src); status != 0 || len(diagCodes(out)) != 0 {
		t.Fatalf("mk check: exit %d\n%s", status, out)
	}
}

// TestSTDDYN_ObjectHoldsLoan: a trait object with a lifetime. DeviceDyn.Of
// returns `DeviceDyn from x`, so the DeviceDyn holds the exclusive loan of
// what it boxes: returning one made from a local is E0603, and reading the
// object while the DeviceDyn is live is E0504. Once its last use is past,
// the object is free again (accept).
func TestSTDDYN_ObjectHoldsLoan(t *testing.T) {
	t.Parallel()
	base := kernelDevice + "\n" + uartDecl
	for _, c := range []struct{ name, body, want string }{
		{"escape", `
Leak(): DeviceDyn {
    var u := Uart(buf: [0; 64], n: 0)
    return DeviceDyn.Of(mut u)
}
`, "39 E0603"},
		{"live", `
Peek(u: mut Uart): int {
    var d := DeviceDyn.Of(u)
    k := u.n
    return d.Write("ab".AsBytes()) + k
}
`, "39 E0504"},
		{"accept", `
After(u: mut Uart): int {
    var d := DeviceDyn.Of(u)
    w := d.Write("ab".AsBytes())
    return w + u.n
}

Scoped(u: mut Uart): int {
    if u.n == 0 {
        var d := DeviceDyn.Of(u)
        _ = d.Write("a".AsBytes())
    }
    u.n += 1
    return u.n
}
`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, _ := dynCheck(t, base+c.body)
			if got := strings.Join(diagCodes(out), ","); got != c.want {
				t.Errorf("want %q, got %q\n%s", c.want, got, out)
			}
		})
	}
}

// TestSTDDYN_Refusals: every interface the reference's E0903 rows name, and
// the name collisions E0206 would otherwise report inside E0905, is one
// E0903 at the interface (the gen line's argument), naming dyn.Of, the
// generator and the gen line; never E0905, which would blame std.
func TestSTDDYN_Refusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, decl, msg string }{
		{"struct", "struct I {\n    x: int\n}", "dyn.Of: needs an interface"},
		{"empty", "interface I {\n}", "the interface has no method"},
		{"value", "interface I {\n    Close(self)\n}", "takes self by value"},
		{"static", "interface I {\n    Count(): int\n}", "has no receiver"},
		{"generic", "interface I {\n    Map[U](&self, u: U): int\n}", "has type parameters"},
		{"flow", "interface I {\n    Name(&self): str from self\n}", "declares a flow clause"},
		{"const", "interface I {\n    At(&self, const n: int): int\n}", "has a const parameter"},
		{"self-param", "interface I {\n    Same(&self, other: &Self): bool\n}", "mentions Self"},
		{"self-slice", "interface I {\n    Many(&self, xs: []Self): int\n}", "mentions Self"},
		{"self-result", "interface I {\n    Dup(&self): ?Self\n}", "mentions Self"},
		{"self-in-fn", "interface I {\n    Each(&self, g: fn(&Self): int): int\n}", "mentions Self"},
		{"of", "interface I {\n    Of(&self): int\n}", "is Of, Obj or Table or starts with Adapt"},
		{"obj", "interface I {\n    Obj(&self): int\n}", "is Of, Obj or Table or starts with Adapt"},
		{"table", "interface I {\n    Table(mut self)\n}", "is Of, Obj or Table or starts with Adapt"},
		{"adapt", "interface I {\n    AdaptWrite(&self): int\n}", "is Of, Obj or Table or starts with Adapt"},
		{"later", "interface I {\n    Ok(&self): int\n    Bad(self)\n}", "takes self by value"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, _ := dynCheck(t, "import std.dyn\n\n"+c.decl+"\n\ngen dyn.Of(I)\n")
			if got := strings.Join(diagCodes(out), ","); got != "3 E0903" {
				t.Errorf("want one E0903 at the interface (line 3), got %q\n%s", got, out)
			}
			if !strings.Contains(out, c.msg) || !strings.Contains(out, "generator: dyn.Of") {
				t.Errorf("want %q from generator dyn.Of\n%s", c.msg, out)
			}
			if strings.Contains(out, "E0905") {
				t.Errorf("dyn.Of emitted invalid code:\n%s", out)
			}
		})
	}
	// The near misses are accepted: Object folds to neither obj nor of,
	// Adapter-free names pass, and Table_s is not Table.
	ok := "import std.dyn\n\ninterface I {\n    Object(&self): int\n    Ofs(&self): int\n    Tables(mut self)\n    Adap(&self): int\n}\n\ngen dyn.Of(I)\n"
	if out, status := dynCheck(t, ok); status != 0 || len(diagCodes(out)) != 0 {
		t.Errorf("near-miss names: exit %d\n%s", status, out)
	}
}

// TestSTDDYN_WarmCache: a second mk check of an edited program with the
// same cache gives the first one's verdict. The second run loads std.dyn
// from the cache through resolve's Session.Load, which rebuilds the
// module's own name for its quotes but no import binding, so the output
// must name nothing through an import of std.dyn (the quotes name
// `dyn.Ref`, the module's own name, which survives a cache load).
func TestSTDDYN_WarmCache(t *testing.T) {
	t.Parallel()
	r := newDynRoot(t, nil)
	r.write(t, "main.mk", kernelDevice+"\nexport Main(sys: &System) {\n}\n")
	if out, status := r.mk(t, "check", "main.mk"); status != 0 {
		t.Fatalf("cold mk check: exit %d\n%s", status, out)
	}
	r.write(t, "main.mk", kernelDevice+"\nexport Main(sys: &System) {\n    _ = 1\n}\n")
	out, status := r.mk(t, "check", "main.mk")
	if status != 0 {
		t.Fatalf("warm mk check: exit %d\n%s", status, out)
	}
}

// TestSTDDYN_ErasedIsSealed: the parts of std.dyn's Ref a user module can
// reach are its four exported methods (Of, Mut, Get, Is): Ref's fields are private (a
// struct literal and a field read are refused), and a Ref is a view holding
// its source's exclusive loan.
func TestSTDDYN_ErasedIsSealed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, body, want string }{
		{"read-p", "Peek(r: &dyn.Ref): *u8 {\n    return r.p\n}\n", "E0204"},
		{"literal", "Forge(p: *u8): dyn.Ref {\n    return dyn.Ref(p: p, key: 0)\n}\n", "E0204"},
		{"loan", "Hold(): bool {\n    var x := 1\n    var r := dyn.Ref.Of(mut x)\n    x += 1\n    return r.Is[int]()\n}\n", "E0504"},
		{"ok", "Is(): bool {\n    var x := 1\n    r := dyn.Ref.Of(mut x)\n    return r.Is[int]() && !r.Is[u8]()\n}\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, _ := dynCheck(t, "import std.dyn\n\n"+c.body)
			codes := diagCodes(out)
			if c.want == "" {
				if len(codes) != 0 {
					t.Errorf("want no diagnostic\n%s", out)
				}
				return
			}
			found := false
			for _, code := range codes {
				found = found || strings.HasSuffix(code, " "+c.want)
			}
			if !found {
				t.Errorf("want %s, got %v\n%s", c.want, codes, out)
			}
		})
	}
}
