// Why: the cache keys objects by the IR text, so the same calls must give
// the same bytes, and printing a large module must stay cheap.
//
// What: Module.WriteTo, which prints globals, functions and then the
// metadata sorted by number, and its helpers (printUsed among them).
//
// How: the module is streamed through one reused chunk buffer (text.go),
// and each block's text is written as it was built, with no fmt and no
// intermediate copy of a function.

package llvm

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// WriteTo prints the module as LLVM 18 textual IR. It fails, writing
// nothing, when a defined function has no block or a block has no
// terminator; otherwise it returns the writer's error. Printing never
// changes the module; it reuses one chunk buffer kept by the module.
func (m *Module) WriteTo(w io.Writer) (int64, error) {
	for _, f := range m.funcs {
		if f.decl {
			continue
		}
		if len(f.blocks) == 0 {
			return 0, fmt.Errorf("llvm: function %s has no blocks", f.ref)
		}
		for _, b := range f.blocks {
			if !b.term {
				return 0, fmt.Errorf("llvm: block %s of %s has no terminator", b.ref, f.ref)
			}
		}
	}
	if m.pbuf == nil {
		m.pbuf = make([]byte, 0, printChunk)
	}
	p := printer{w: w, buf: m.pbuf[:0]}
	m.print(&p)
	p.flush()
	m.pbuf = p.buf[:0]
	return p.n, p.err
}

// String is the printed module; it panics where WriteTo fails.
func (m *Module) String() string {
	var sb strings.Builder
	if _, err := m.WriteTo(&sb); err != nil {
		panic(err.Error())
	}
	return sb.String()
}

// appendLink appends " linkage dso_local visibility", each part only when
// it applies.
func appendLink(buf []byte, l Linkage, dsoLocal bool, vis Visibility) []byte {
	if s := l.String(); s != "" {
		buf = append(buf, ' ')
		buf = append(buf, s...)
	}
	if dsoLocal && !l.local() && vis == Default {
		buf = append(buf, " dso_local"...)
	}
	if s := vis.String(); s != "" {
		buf = append(buf, ' ')
		buf = append(buf, s...)
	}
	return buf
}

func (m *Module) print(p *printer) {
	b := p.buf
	b = append(b, "source_filename = "...)
	b = appendQuoted(b, m.sourceFilename)
	b = append(b, '\n')
	if m.dataLayout != "" {
		b = append(b, "target datalayout = "...)
		b = appendQuoted(b, m.dataLayout)
		b = append(b, '\n')
	}
	if m.triple != "" {
		b = append(b, "target triple = "...)
		b = appendQuoted(b, m.triple)
		b = append(b, '\n')
	}
	if len(m.asm) > 0 {
		b = append(b, '\n')
		for _, l := range m.asm {
			b = append(b, "module asm "...)
			b = appendQuoted(b, l)
			b = append(b, '\n')
		}
	}
	if len(m.comdats) > 0 {
		b = append(b, '\n')
		for _, c := range m.comdats {
			b = append(b, '$')
			b = append(b, Quote(c)...)
			b = append(b, " = comdat nodeduplicate\n"...)
		}
	}
	if len(m.globals) > 0 || len(m.used) > 0 || len(m.cused) > 0 {
		b = append(b, '\n')
	}
	p.buf = b
	p.line()
	for _, g := range m.globals {
		m.printGlobal(p, g)
	}
	printUsed(p, "llvm.used", m.used)
	printUsed(p, "llvm.compiler.used", m.cused)
	if len(m.aliases) > 0 {
		p.buf = append(p.buf, '\n')
	}
	for _, a := range m.aliases {
		b = append(p.buf, '@')
		b = append(b, Quote(a.name)...)
		b = append(b, " ="...)
		b = appendLink(b, a.linkage, false, a.vis)
		b = append(b, " alias "...)
		b = append(b, a.valueTy.String()...)
		b = append(b, ", "...)
		b = appendTyped(b, a.target)
		b = append(b, '\n')
		p.buf = b
		p.line()
	}
	for _, f := range m.funcs {
		p.buf = append(p.buf, '\n')
		m.printFunc(p, f)
	}
	if m.dbgDeclare {
		p.buf = append(p.buf, "\ndeclare void @llvm.dbg.declare(metadata, metadata, metadata)\n"...)
		p.line()
	}
	if m.noaliasDecl {
		p.buf = append(p.buf, "\ndeclare void @llvm.experimental.noalias.scope.decl(metadata)\n"...)
		p.line()
	}
	mt := m.meta
	if len(mt.named) > 0 || len(mt.flags) > 0 || len(mt.nodes) > 0 {
		p.buf = append(p.buf, '\n')
	}
	for _, n := range mt.named {
		printNamed(p, n.name, n.nodes)
	}
	if len(mt.flags) > 0 {
		printNamed(p, "llvm.module.flags", mt.flags)
	}
	for i := range mt.nodes {
		b = append(p.buf, '!')
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, " = "...)
		b = mt.appendNode(b, i)
		b = append(b, '\n')
		p.buf = b
		p.line()
	}
}

// printNamed prints the named metadata !name = !{!a, !b}.
func printNamed(p *printer, name string, nodes []MD) {
	b := append(p.buf, '!')
	b = append(b, name...)
	b = append(b, " = !{"...)
	for i, md := range nodes {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = appendMDRef(b, md)
	}
	p.buf = append(b, "}\n"...)
	p.line()
}

func (m *Module) printGlobal(p *printer, g *Global) {
	a := g.a
	b := append(p.buf, g.ref...)
	b = append(b, " ="...)
	if a.Init.IsZero() {
		b = append(b, " external"...)
	}
	b = appendLink(b, a.Linkage, a.DSOLocal, a.Visibility)
	if a.UnnamedAddr {
		b = append(b, " unnamed_addr"...)
	}
	if a.Constant {
		b = append(b, " constant "...)
	} else {
		b = append(b, " global "...)
	}
	b = append(b, a.Type.String()...)
	p.buf = b
	if !a.Init.IsZero() {
		p.buf = append(p.buf, ' ')
		if a.Init.kind == vkText {
			p.str(a.Init.ref) // a large initializer goes to the writer directly
		} else {
			p.buf = appendRef(p.buf, a.Init)
		}
	}
	b = p.buf
	if a.Section != "" {
		b = append(b, ", section "...)
		b = appendQuoted(b, a.Section)
	}
	if a.Comdat != "" {
		b = append(b, ", comdat($"...)
		b = append(b, Quote(a.Comdat)...)
		b = append(b, ')')
	}
	b = appendAlign(b, a.Align)
	if !g.assoc.IsZero() {
		b = append(b, ", !associated "...)
		b = appendMDRef(b, g.assoc)
	}
	b = append(b, '\n')
	p.buf = b
	p.line()
}

func (m *Module) printFunc(p *printer, f *Function) {
	s := f.sig
	b := p.buf
	if f.decl {
		b = append(b, "declare"...)
	} else {
		b = append(b, "define"...)
	}
	b = appendLink(b, s.Linkage, s.DSOLocal, s.Visibility)
	if d := s.DLLStorage.String(); d != "" {
		b = append(b, ' ')
		b = append(b, d...)
	}
	if cc := s.CC.String(); cc != "" {
		b = append(b, ' ')
		b = append(b, cc...)
	}
	b = appendSpAttrs(b, s.RetAttrs)
	b = append(b, ' ')
	b = append(b, s.Ret.String()...)
	b = append(b, ' ')
	b = append(b, f.ref...)
	b = append(b, '(')
	for i, t := range s.Params {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = append(b, t.String()...)
		if len(s.ParamAttrs) != 0 {
			b = appendSpAttrs(b, s.ParamAttrs[i])
		}
		if !f.decl {
			b = append(b, ' ')
			b = appendRef(b, f.params[i])
		}
	}
	if s.Variadic {
		if len(s.Params) > 0 {
			b = append(b, ", "...)
		}
		b = append(b, "..."...)
	}
	b = append(b, ')')
	b = appendSpAttrs(b, s.FnAttrs)
	b = appendSpAttrs(b, f.defAttrs)
	if f.section != "" {
		b = append(b, " section "...)
		b = appendQuoted(b, f.section)
	}
	if f.comdat != "" {
		b = append(b, " comdat($"...)
		b = append(b, Quote(f.comdat)...)
		b = append(b, ')')
	}
	if !f.dbg.IsZero() {
		b = append(b, " !dbg "...)
		b = appendMDRef(b, f.dbg)
	}
	if !f.prof.IsZero() {
		b = append(b, " !prof "...)
		b = appendMDRef(b, f.prof)
	}
	if f.decl {
		p.buf = append(b, '\n')
		p.line()
		return
	}
	p.buf = append(b, " {\n"...)
	for i, blk := range f.blocks {
		if i > 0 {
			p.buf = append(p.buf, '\n')
		}
		p.buf = append(p.buf, blk.ref[1:]...) // the quoted label
		p.buf = append(p.buf, ":\n"...)
		p.bytes(blk.text)
	}
	p.buf = append(p.buf, "}\n"...)
	p.line()
}

// printUsed prints @name = appending global [n x ptr] [...], section
// "llvm.metadata", for a non-empty list.
func printUsed(p *printer, name string, vs []Value) {
	if len(vs) == 0 {
		return
	}
	b := append(p.buf, '@')
	b = append(b, name...)
	b = append(b, " = appending global ["...)
	b = strconv.AppendInt(b, int64(len(vs)), 10)
	b = append(b, " x ptr] ["...)
	for i, v := range vs {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = appendTyped(b, v)
	}
	b = append(b, "], section \"llvm.metadata\"\n"...)
	p.buf = b
	p.line()
}
