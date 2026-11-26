// Why: a Linux static library must be a GNU archive with a symbol index,
// and the Linux bundle ships no archiver.
//
// What: WriteArchive, which writes the archive, and arMembers, which reads
// one back (so an input archive's members join the output).
//
// How: written in Go, deterministic (date, owner and group 0, mode 644,
// members named o%04d.o), with an index of every defined global or weak
// symbol read with debug/elf.

package toolchain

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WriteArchive writes out, a GNU archive of objs in order: the symbol
// index member "/" (big-endian 32-bit counts and offsets, then the names),
// the long-name member "//" when a member's name is over 15 bytes, then
// each object, deterministic (date, owner and group 0, mode 644). Member i
// is named o%04d.o, as COFF's StaticLib names its staged inputs, so the
// archive does not depend on the inputs' file names: an object from the
// cache and a fresh one have different names. An input that
// is itself a GNU archive (a package's static link) adds each of its
// object members. The index lists every defined global or weak symbol of
// each object, read with debug/elf, so lld (or any GNU linker) pulls a
// member by the name a reference needs.
func WriteArchive(out string, objs []string) error {
	type member struct {
		name string
		data []byte
		syms []string
	}
	var ms []member
	var inputs []namedData
	for _, o := range objs {
		data, err := os.ReadFile(o)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(data, []byte("!<arch>\n")) {
			inputs = append(inputs, namedData{o, data})
			continue
		}
		mems, err := arMembers(data)
		if err != nil {
			return fmt.Errorf("toolchain: %s: %v", o, err)
		}
		for _, m := range mems {
			inputs = append(inputs, namedData{o + "(" + m.name + ")", m.data})
		}
	}
	for _, in := range inputs {
		o, data := in.name, in.data
		f, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("toolchain: %s is not an ELF object: %v", o, err)
		}
		syms, err := f.Symbols()
		f.Close()
		if err != nil && err != elf.ErrNoSymbols {
			return fmt.Errorf("toolchain: %s: %v", o, err)
		}
		var names []string
		for _, s := range syms {
			bind, typ := elf.ST_BIND(s.Info), elf.ST_TYPE(s.Info)
			if s.Section == elf.SHN_UNDEF || s.Name == "" || (bind != elf.STB_GLOBAL && bind != elf.STB_WEAK) ||
				typ == elf.STT_FILE || typ == elf.STT_SECTION {
				continue
			}
			names = append(names, s.Name)
		}
		ms = append(ms, member{name: fmt.Sprintf("o%04d.o", len(ms)), data: data, syms: names})
	}
	// The long-name table: names of 16 bytes or more, each "name/\n".
	var long bytes.Buffer
	ids := make([]string, len(ms))
	for i, m := range ms {
		if len(m.name) <= 15 && !strings.ContainsAny(m.name, "/ ") {
			ids[i] = m.name + "/"
			continue
		}
		ids[i] = "/" + strconv.Itoa(long.Len())
		long.WriteString(m.name + "/\n")
	}
	header := func(b *bytes.Buffer, name string, size int) {
		fmt.Fprintf(b, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", name, "0", "0", "0", "644", size)
	}
	pad := func(n int) int { return n + n%2 }
	// The index's size fixes every member's offset, so it comes first.
	nsyms := 0
	var strtab bytes.Buffer
	for _, m := range ms {
		nsyms += len(m.syms)
		for _, s := range m.syms {
			strtab.WriteString(s)
			strtab.WriteByte(0)
		}
	}
	indexSize := 4 + 4*nsyms + strtab.Len()
	off := 8 + 60 + pad(indexSize)
	if long.Len() > 0 {
		off += 60 + pad(long.Len())
	}
	if off+60*len(ms) > 1<<32-1 {
		return fmt.Errorf("toolchain: archive %s is over 4 GiB", out)
	}
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	header(&b, "/", indexSize)
	var word [4]byte
	binary.BigEndian.PutUint32(word[:], uint32(nsyms))
	b.Write(word[:])
	pos := off
	for _, m := range ms {
		for range m.syms {
			binary.BigEndian.PutUint32(word[:], uint32(pos))
			b.Write(word[:])
		}
		pos += 60 + pad(len(m.data))
		if pos > 1<<32-1 {
			return fmt.Errorf("toolchain: archive %s is over 4 GiB", out)
		}
	}
	b.Write(strtab.Bytes())
	if indexSize%2 == 1 {
		b.WriteByte('\n')
	}
	if long.Len() > 0 {
		header(&b, "//", long.Len())
		b.Write(long.Bytes())
		if long.Len()%2 == 1 {
			b.WriteByte('\n')
		}
	}
	for i, m := range ms {
		header(&b, ids[i], len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	// A temporary of this write's own, so two builds writing out at once
	// never write into one file. On Windows a rename over out fails while
	// another build renames over it or reads it, so it is retried, briefly.
	f, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(0o644) // a library others may read, as os.WriteFile made it
	if err == nil {
		_, err = f.Write(b.Bytes())
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		for try := 1; ; try++ {
			if err = os.Rename(tmp, out); err == nil || try == renameTries {
				break
			}
			time.Sleep(time.Duration(try) * time.Millisecond)
		}
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	removeStaleTemps(out)
	return nil
}

// renameTries bounds WriteArchive's renames: their pauses add up to about
// a second.
const renameTries = 45

// removeStaleTemps removes the temporaries of out ('.<base>.tmp-*') older
// than staleTempAge: a writer killed between its temporary and its rename
// leaves one, each with a name of its own, beside the library. A live
// writer's temporary is seconds old at most.
func removeStaleTemps(out string) {
	dir, prefix := filepath.Dir(out), "."+filepath.Base(out)+".tmp-"
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	old := time.Now().Add(-staleTempAge)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), prefix) || !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(old) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// staleTempAge is the age past which removeStaleTemps takes a temporary
// for a killed writer's.
const staleTempAge = 10 * time.Minute

// namedData is a file's bytes and the name an error gives it.
type namedData struct {
	name string
	data []byte
}

// arMembers is the members of the GNU archive data,
// in order, less the symbol index ("/", "/SYM64/") and the long-name table
// ("//"); a long name ("/123") is read from that table.
func arMembers(data []byte) ([]namedData, error) {
	var out []namedData
	var long []byte
	for at := 8; at < len(data); {
		if at+60 > len(data) {
			return nil, fmt.Errorf("archive truncated at %d", at)
		}
		h := data[at : at+60]
		size, err := strconv.Atoi(strings.TrimSpace(string(h[48:58])))
		if err != nil || size < 0 || at+60+size > len(data) {
			return nil, fmt.Errorf("bad archive member header at %d", at)
		}
		name := strings.TrimSpace(string(h[:16]))
		body := data[at+60 : at+60+size]
		at += 60 + size + size%2
		switch {
		case name == "/" || name == "/SYM64/":
		case name == "//":
			long = body
		case strings.HasPrefix(name, "/"):
			off, err := strconv.Atoi(name[1:])
			if err != nil || off >= len(long) {
				return nil, fmt.Errorf("bad long member name %q", name)
			}
			end := bytes.IndexByte(long[off:], '\n')
			if end < 0 {
				end = len(long) - off
			}
			out = append(out, namedData{strings.TrimSuffix(string(long[off:off+end]), "/"), body})
		default:
			out = append(out, namedData{strings.TrimSuffix(name, "/"), body})
		}
	}
	return out, nil
}
