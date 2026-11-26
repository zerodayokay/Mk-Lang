// Why: every code the checker emits has a fixed JSON form, fixes and an
// order that tools and caches rely on.
// What: one JSON golden per emitted code (TestGoldens), TestCodeTable, the
// fix tests, TestDeterministicAcrossRuns and TestLoanIDOrder.
// How: triggers builds one body per code; goldens live in testdata and the
// code list is compared with every diag.E constant the package names.

package borrow

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

var update = flag.Bool("update", false, "rewrite golden files")

// borrowCodes is the table of every code Check can emit, and no other
// (E0407 and E0606 do not exist; E0408, E0807, E1001 and E1002 are check's;
// E0802 is check's too, but for a typed E!T binding's lifetime).
// E0609 covers the result-loan, store and backstop forms.
var borrowCodes = []diag.Code{diag.E0401, diag.E0402, diag.E0403, diag.E0404, diag.E0405, diag.E0406,
	diag.E0409, diag.E0411, diag.E0501, diag.E0502, diag.E0503, diag.E0504, diag.E0505, diag.E0506, diag.E0507,
	diag.E0508, diag.E0602, diag.E0603, diag.E0604, diag.E0605, diag.E0609, diag.E0802, diag.E1102}

// kitSources is a source map whose file 1 has a line per 10 bytes, so the
// kit's span n is line n+1, column 1.
func kitSources() *source.SourceMap {
	sm := source.NewSourceMap()
	sm.Add("t/borrow.mk", []byte(strings.Repeat("    abcd \n", 4000)))
	return sm
}

// triggers builds a minimal body for each code.
func triggers() map[diag.Code]func(t *testing.T) (*kit, *mb) {
	k0 := func(t *testing.T) *kit { return newKit(t) }
	return map[diag.Code]func(t *testing.T) (*kit, *mb){
		diag.E0401: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Moves", nil, types.Void, types.EmptyErrors))
			f := m.user("f", k.Res, false)
			g := m.user("g", k.Res, false)
			m.acquire(f)
			m.set(g, use(mv(m.local(f))))
			m.close(f)
			m.close(g)
			m.end(g, f)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0402: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Drop", nil, types.Void, types.EmptyErrors))
			f := m.user("f", k.Res, false)
			m.acquire(f)
			m.end(f)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0403: func(t *testing.T) (*kit, *mb) { m, _ := lateBody(t, false); return m.k, m },
		diag.E0802: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			u := k.tab.ErrorUnion(k.errorItem("Bad"), types.I32)
			m := k.body(k.sig("Over", []types.Param{k.param("a", u)}, types.Void, types.EmptyErrors))
			c := m.user("c", u, true)
			m.set(c, use(cp(m.local(1))))
			m.set(c, use(cp(m.local(1))))
			tmp := m.temp(u)
			m.set(tmp, use(cp(m.local(c))))
			m.end(tmp, c)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0404: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Twice", nil, types.Void, types.EmptyErrors))
			x := m.user("x", types.I32, false)
			m.b.Locals[x].Span = m.set(x, use(imm(types.I32, 1))) // x := 1 declares x
			m.set(x, use(imm(types.I32, 2)))
			m.end(x)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0405: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Loop", []types.Param{k.param("c", types.Bool)}, types.Void, types.EmptyErrors))
			f := m.user("f", k.Res, false)
			m.acquire(f)
			header, body, exit := m.block(), m.block(), m.block()
			m.b.Loops = append(m.b.Loops, mir.LoopInfo{Header: header, Exit: exit, Holder: mir.NoLocal, Span: m.sp()})
			m.jump(header)
			m.at(header)
			m.branch(cp(m.local(1)), body, exit)
			m.at(body)
			m.callv("Take", []types.TypeID{k.Res}, argMove(m.local(f)))
			m.jump(header)
			m.at(exit)
			m.end(f)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0406: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Deferred", nil, types.Void, types.EmptyErrors))
			f := m.user("f", k.Res, false)
			m.acquire(f)
			m.b.Defers = append(m.b.Defers, mir.DeferInfo{Span: m.sp()})
			m.callv("Take", []types.TypeID{k.Res}, argMove(m.local(f)))
			d := m.b.AddBlock(mir.OriginDefer, 0)
			m.jump(d)
			m.at(d)
			m.close(f)
			m.end(f)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0409: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			bt := k.both()
			m := k.body(k.sig("Leak", []types.Param{k.param("p", bt)}, k.Res, types.EmptyErrors))
			m.set(0, use(mv(m.pl(1, fld(0, k.Res)))))
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0411: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			lt := listT(k)
			m := k.body(k.fn("F", []types.Param{k.param("p", lt)}, types.Void, k.errs("Full")))
			m.callv("Take", []types.TypeID{lt}, argMove(m.local(1)))
			m.fail("Full")
			return k, m
		},
		diag.E0501: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Same", nil, types.Void, types.EmptyErrors))
			mi := k.tab.Mut(types.I32)
			n := m.user("n", types.I32, true)
			m.set(n, use(imm(types.I32, 1)))
			t1, t2 := m.temp(mi), m.temp(mi)
			m.set(t1, mutRef(m.local(n)))
			m.set(t2, mutRef(m.local(n)))
			m.callv("Exchange", []types.TypeID{types.I32}, argMove(m.local(t1)), argMove(m.local(t2)))
			m.end(t2, t1, n)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0502: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Frozen", nil, types.Void, types.EmptyErrors))
			n := m.user("n", types.I32, true)
			a := m.user("a", refI32(k), false)
			m.set(n, use(imm(types.I32, 1)))
			m.set(a, ref(m.local(n)))
			m.set(n, use(imm(types.I32, 2)))
			m.callv("Use", []types.TypeID{types.I32}, argCopy(m.local(a)))
			m.end(a, n)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0503: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("F", nil, types.Void, types.EmptyErrors))
			f := m.user("f", k.Res, false)
			r := m.user("r", k.tab.Ref(k.Res), false)
			m.acquire(f)
			m.set(r, ref(m.local(f)))
			m.close(f)
			m.callv("Use", []types.TypeID{k.Res}, argCopy(m.local(r)))
			m.end(r, f)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0504: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("F", nil, types.Void, types.EmptyErrors))
			n := m.user("n", types.I32, true)
			r := m.user("r", k.tab.Mut(types.I32), false)
			m.set(n, use(imm(types.I32, 1)))
			m.set(r, mutRef(m.local(n)))
			m.useRef(m.local(n), types.I32)
			m.callv("UseMut", []types.TypeID{types.I32}, argMove(m.local(r)))
			m.end(r, n)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0505: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("F", nil, types.Void, types.EmptyErrors))
			out := m.user("out", refI32(k), true)
			long := m.user("long", types.I32, false)
			m.set(long, use(imm(types.I32, 1)))
			m.set(out, ref(m.local(long)))
			short := m.user("short", types.I32, false)
			m.set(short, use(imm(types.I32, 2)))
			m.set(out, ref(m.local(short)))
			m.end(short)
			m.callv("Use", []types.TypeID{types.I32}, argCopy(m.local(out)))
			m.end(long, out)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0506: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("TooLong", nil, types.Void, types.EmptyErrors))
			r := m.user("r", k.tab.Ref(k.Point), false)
			tp := m.temp(k.Point)
			nx := m.block()
			m.call(k.item("MakePoint"), nil, nil, m.local(tp), nx)
			m.at(nx)
			m.set(r, ref(m.local(tp)))
			m.end(tp)
			m.callv("Use", []types.TypeID{k.Point}, argCopy(m.local(r)))
			m.end(r)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0507: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("F", []types.Param{k.param("r", refI32(k))}, types.Void, types.EmptyErrors))
			m.assign(m.pl(1, deref(types.I32)), use(imm(types.I32, 1)))
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0508: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("F", nil, types.Void, types.EmptyErrors))
			n := m.user("n", types.I32, false)
			r := m.user("r", k.tab.Mut(types.I32), false)
			m.b.Locals[n].Span = m.set(n, use(imm(types.I32, 1))) // n := 1 declares n
			m.set(r, mutRef(m.local(n)))
			m.callv("UseMut", []types.TypeID{types.I32}, argMove(m.local(r)))
			m.end(r, n)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0602: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			s := k.sig("Pick", []types.Param{k.param("a", types.Str), k.param("b", types.Str)}, types.Str, types.EmptyErrors)
			s.Flows.Sources = []types.Source{{Param: 1}}
			m := k.body(s)
			m.set(0, use(cp(m.local(2))))
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0603: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			m := k.body(k.sig("Dangling", nil, refI32(k), types.EmptyErrors))
			n := m.user("n", types.I32, false)
			m.set(n, use(imm(types.I32, 1)))
			m.set(0, ref(m.local(n)))
			m.end(n)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0604: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			ri := refI32(k)
			m := k.body(k.sig("Put", []types.Param{k.param("out", k.tab.Mut(ri)), k.param("v", ri)}, types.Void, types.EmptyErrors))
			m.assign(m.pl(1, deref(ri)), use(cp(m.local(2))))
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0605: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			ori := refI32(k)
			cellT := k.tab.Named(k.known.Cell, []types.GenericArg{types.TypeArg(ori)})
			gi := k.items.AddItem(&types.Item{Module: k.main.ID, Name: k.in.Intern("G"), Kind: types.KindGlobal,
				Key: types.ItemKey("main", "G"), Decl: &types.GlobalDecl{Type: cellT}})
			k.lib()
			m := k.body(k.sig("F", nil, types.Void, types.EmptyErrors))
			n := m.user("n", types.I32, false)
			m.set(n, use(imm(types.I32, 1)))
			g := m.temp(k.tab.Ref(cellT))
			r := m.temp(ori)
			m.set(g, mir.Rvalue{Kind: mir.RvGlobalRef, Item: gi})
			m.set(r, ref(m.local(n)))
			m.callv("Cell.Store", []types.TypeID{ori}, argCopy(m.local(g)), argCopy(m.local(r)))
			m.end(r, g, n)
			m.ret(mir.ExitNormal)
			return k, m
		},
		diag.E0609: func(t *testing.T) (*kit, *mb) {
			// A capturing literal returning a captured str (the result-loan
			// form).
			k := capKit{k0(t)}
			rec, rf := k.env([]string{"prefix"}, []types.TypeID{types.Str})
			m, env := k.literalBody("Prefix", []types.Param{k.param("a", k.tab.Ref(types.Int))}, types.Str, rf)
			m.set(0, use(cp(k.capturePlace(m, env, rec, 0))))
			m.ret(mir.ExitNormal)
			return k.kit, m
		},
		diag.E1102: func(t *testing.T) (*kit, *mb) {
			k := k0(t)
			s := k.method("Res", "Free", types.ByValue, k.Res, nil, types.Void, types.EmptyErrors)
			s.Flows.Ends = []types.ParamIndex{1}
			s.Span = source.Span{File: kitFile, Start: 2, End: 3}
			m := k.body(s)
			m.callv("Take", []types.TypeID{k.Res}, argMove(m.local(1)))
			m.ret(mir.ExitNormal)
			return k, m
		},
	}
}

// TestCodeTable: borrowCodes equals the codes the package's source can
// emit (every diag.E constant named in a non-test file), and each has a
// golden.
func TestCodeTable(t *testing.T) {
	re := regexp.MustCompile(`diag\.(E\d{4})`)
	files, _ := filepath.Glob("*.go")
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllSubmatch(b, -1) {
			seen[string(m[1])] = true
		}
	}
	var src []string
	for c := range seen {
		src = append(src, c)
	}
	slices.Sort(src)
	var want []string
	for _, c := range borrowCodes {
		want = append(want, c.String())
	}
	if !slices.Equal(src, want) {
		t.Fatalf("codes in the package's source %v, the code table lists %v", src, want)
	}
	tr := triggers()
	for _, c := range borrowCodes {
		if tr[c] == nil {
			t.Errorf("%v has no golden trigger", c)
		}
	}
}

// TestGoldens writes or compares one JSON golden per code: the minimal
// trigger's report through diag.Order and WriteJSON.
func TestGoldens(t *testing.T) {
	tr := triggers()
	for _, c := range borrowCodes {
		t.Run(c.String(), func(t *testing.T) {
			k, m := tr[c](t)
			m.validate(t)
			sm := kitSources()
			env := k.env(m.sig)
			env.File = func(id source.FileID) *source.File { return sm.File(id) }
			res := Check(m.b, env)
			found := false
			for _, d := range res.Diags {
				found = found || d.Code == c
			}
			if !found {
				t.Fatalf("trigger gives %v, want %v", got(res), c)
			}
			js := diag.Order(res.Diags, sm, diag.NoCap).WriteJSON(sm)
			path := filepath.Join("testdata", strings.ToLower(c.String())+".golden")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, js, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/borrow -run TestGoldens -update)", err)
			}
			if !bytes.Equal(bytes.TrimRight(want, "\r\n"), bytes.TrimRight(js, "\n")) {
				t.Fatalf("%s differs:\n got %s\nwant %s", path, js, want)
			}
		})
	}
}

// TestFixAddVar: E0404 and E0508 carry the proven "add var" fix at the
// declaration.
func TestFixAddVar(t *testing.T) {
	for _, c := range []diag.Code{diag.E0404, diag.E0508} {
		k, m := triggers()[c](t)
		res := m.check(t)
		d := res.Diags[0]
		decl := m.b.Span(m.b.Locals[m.b.ParamCount+1].Span)
		if d.Code != c || len(d.Fixes) != 1 || d.Fixes[0].Class != diag.Proven || len(d.Fixes[0].Edits) != 1 ||
			d.Fixes[0].Edits[0].Text != "var " || d.Fixes[0].Edits[0].Span.Start != decl.Start || !d.Fixes[0].Edits[0].Span.IsEmpty() {
			t.Fatalf("%v: fixes %+v", c, d.Fixes)
		}
		_ = k
	}
}

// TestFixAddVar_OmittedForReceiver: P.Bump(self) { self.x = 1 // <- E0404 }
// has no fix and the alternative `var s := self`.
func TestFixAddVar_OmittedForReceiver(t *testing.T) {
	k := newKit(t)
	s := k.method("Point", "Bump", types.ByValue, k.Point, nil, types.Void, types.EmptyErrors)
	m := k.body(s)
	bad := m.assign(m.pl(1, fld(0, types.I32)), use(imm(types.I32, 1)))
	m.ret(mir.ExitNormal)
	res := m.check(t)
	m.expect(t, res, diag.E0404, bad)
	d := res.Diags[0]
	if len(d.Fixes) != 0 || len(d.Alternatives) != 1 || !strings.Contains(d.Alternatives[0], "var s := self") {
		t.Fatalf("receiver: fixes %+v alternatives %v", d.Fixes, d.Alternatives)
	}
}

// TestFixBindLocal_E0506: the "bind a local" fix inserts `tmp := <text>`
// on the statement's line and replaces the temporary's expression.
func TestFixBindLocal_E0506(t *testing.T) {
	k, m := triggers()[diag.E0506](t)
	m.validate(t)
	sm := kitSources()
	env := k.env(m.sig)
	env.File = func(id source.FileID) *source.File { return sm.File(id) }
	res := Check(m.b, env)
	if len(res.Diags) != 1 || len(res.Diags[0].Fixes) != 1 {
		t.Fatalf("diags %v", got(res))
	}
	ed := res.Diags[0].Fixes[0].Edits
	if len(ed) != 2 || !strings.HasPrefix(ed[0].Text, "    tmp := ") || ed[1].Text != "tmp" {
		t.Fatalf("edits %+v", ed)
	}
}

// TestE0402_TargetOneVoidRelease: a named Res local left unconsumed gets
// release [Close] and target `defer f.Close()`.
func TestE0402_TargetOneVoidRelease(t *testing.T) {
	_, m := triggers()[diag.E0402](t)
	res := m.check(t)
	d := res.Diags[0]
	tg, ok := d.FactValue("target")
	if !ok || tg.Str != "defer f.Close()" {
		t.Fatalf("target %+v", d.Facts)
	}
	if rl, ok := d.FactValue("release"); !ok || len(rl.List) != 1 || rl.List[0].Str != "Close" {
		t.Fatalf("release %+v", d.Facts)
	}
}

// TestE0402_NoTargetWithArguments: a consuming method with a parameter gives
// release but no target.
func TestE0402_NoTargetWithArguments(t *testing.T) {
	k := newKit(t)
	k.structItem(k.main, "Pipe", 0, false, true, field("fd", types.I32))
	pt := k.named("Pipe")
	k.method("Pipe", "Close", types.ByValue, pt, []types.Param{k.param("io", k.Io)}, types.Void, types.EmptyErrors)
	m := k.body(k.sig("F", []types.Param{k.param("p", pt)}, types.Void, types.EmptyErrors))
	m.ret(mir.ExitNormal)
	res := m.check(t)
	d := res.Diags[0]
	if _, ok := d.FactValue("target"); ok || d.Code != diag.E0402 {
		t.Fatalf("%v facts %+v", d.Code, d.Facts)
	}
	if _, ok := d.FactValue("release"); !ok {
		t.Fatalf("release missing: %+v", d.Facts)
	}
}

// TestDeterministicAcrossRuns: the same body checked 100 times in parallel
// goroutines gives byte-identical JSON.
func TestDeterministicAcrossRuns(t *testing.T) {
	k := newKit(t)
	k.cellNew()
	m := k.body(k.sig("Multi", []types.Param{k.param("c", types.Bool)}, types.Void, types.EmptyErrors))
	// Several diagnostics of several codes.
	f := m.user("f", k.Res, false)
	g := m.user("g", k.Res, false)
	n := m.user("n", types.I32, true)
	a := m.user("a", refI32(k), false)
	m.acquire(f)
	m.set(g, use(mv(m.local(f))))
	m.close(f)
	m.set(n, use(imm(types.I32, 1)))
	m.set(a, ref(m.local(n)))
	m.set(n, use(imm(types.I32, 2)))
	m.callv("Use", []types.TypeID{types.I32}, argCopy(m.local(a)))
	m.end(a, n, g, f)
	m.ret(mir.ExitNormal)
	m.validate(t)
	sm := kitSources()
	env := k.env(m.sig)
	first := diag.Order(Check(m.b, env).Diags, sm, diag.NoCap).WriteJSON(sm)
	var wg sync.WaitGroup
	outs := make([][]byte, 100)
	for i := range outs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i] = diag.Order(Check(m.b, env).Diags, sm, diag.NoCap).WriteJSON(sm)
		}()
	}
	wg.Wait()
	for i, o := range outs {
		if !bytes.Equal(o, first) {
			t.Fatalf("run %d differs:\n%s\n%s", i, o, first)
		}
	}
	if !bytes.Contains(first, []byte("E0401")) || !bytes.Contains(first, []byte("E0502")) {
		t.Fatalf("expected E0401 and E0502: %s", first)
	}
}

// TestLoanIDOrder: loans are allocated parameters first (outer, inner per
// carrier parameter, in parameter order), then statics, then borrow sites in
// block and statement order.
func TestLoanIDOrder(t *testing.T) {
	k := newKit(t)
	ri := refI32(k)
	lim := k.items.AddItem(&types.Item{Module: k.main.ID, Name: k.in.Intern("Limit"), Kind: types.KindConst,
		Key: types.ItemKey("main", "Limit"), Decl: &types.ConstDecl{Type: types.I32}})
	m := k.body(k.sig("F", []types.Param{k.param("a", ri), k.param("n", types.I32), k.param("b", k.tab.Mut(types.I32))}, types.Void, types.EmptyErrors))
	x := m.user("x", types.I32, false)
	r1 := m.user("r1", ri, false)
	r2 := m.user("r2", ri, false)
	m.set(x, use(imm(types.I32, 1)))
	m.set(r1, ref(m.local(x)))
	m.set(r2, mir.Rvalue{Kind: mir.RvGlobalRef, Item: lim})
	m.end(r2, r1, x)
	m.ret(mir.ExitNormal)
	m.validate(t)
	c := newChecker(m.b, k.env(m.sig))
	c.setup()
	var kinds []string
	for _, l := range c.loans {
		switch {
		case l.abstract && !l.inner:
			kinds = append(kinds, fmt.Sprintf("outer%d", l.param))
		case l.abstract:
			kinds = append(kinds, fmt.Sprintf("inner%d", l.param))
		case l.static:
			kinds = append(kinds, "static")
		default:
			kinds = append(kinds, "site")
		}
	}
	want := []string{"outer1", "inner1", "outer3", "inner3", "static", "site"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("loan order %v, want %v", kinds, want)
	}
}
