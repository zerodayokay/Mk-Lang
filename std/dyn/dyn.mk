// Why: Mk has no built-in dynamic dispatch; std.dyn generates it as a library
// product, so a program that wants interface values over many types gets
// them without a language feature.
//
// What: Of, the gen function behind `gen dyn.Of(I)`, and its text helpers
// (Refusal, FoldMatch, Reserved, Snake, Slot, Join).
//
// How: it runs at compile time only; the code it emits uses the lowest layer
// of std alone, so a freestanding program may use it. Soundness rests on
// ref.mk: a Ref gives its object back only as the type it was made from, so
// a forged table can only panic.
//
// For an interface I, gen dyn.Of(I) emits, in the gen line's module:
// - mut view struct IDyn { obj: dyn.Ref, table: (one fn field per method of
//   I, each the snake_case of its method's name, with `_` after a keyword
//   or contextual word: Error is error_) };
//   the record is the vtable held inline, so a call loads its fn from the
//   object itself and needs no second load through a table pointer; the
//   boxed type is a mut view;
// - IDyn.Of[T: I](x: mut T): IDyn from x, which erases x and fills the
//   fields with IDyn.Adapt<M>[T], generic adapters named as fn values;
// - per method M, the adapter IDyn.Adapt<M>[T: I], which recovers the T
//   from the dyn.Ref (checked against T's key) and calls T's M;
// - impl I for IDyn, one forwarding method per method of I, each a load of
//   its field and one indirect call.
// The adapter's object parameter (obj) and the forwarding method's fn local
// (f) take a digit (obj2, f2) when a parameter of the method folds to them.
// When I is exported, IDyn and IDyn.Of are exported; the adapters stay
// private. dyn.Ref is a literal name naming this module's own export: it
// comes out resolved, so the gen line's module needs no import for it, and
// it survives a cache load of std.dyn (an import binding of a quote does
// not).
//
// Refused with E0903 at I (Gen.FailOn): anything but an interface, an
// interface with no method, and a method with a by-value or no receiver,
// type parameters of its own, a flow clause, a const parameter, Self in a
// parameter or the result, or a name that folds (E0206) to a member the
// output already declares: Of, Obj, Table, or one starting with Adapt.
//
// The text helpers below call Must on the run's arena: its pages come from
// the evaluator, which never refuses one (it charges the bytes as fuel, so a
// runaway run ends in E0902 first), so Must cannot fire outside the
// soundness checker's injected allocation failures.

/// The generator behind `gen dyn.Of(I)`: it writes IDyn, one value type
/// for every type that implements the interface I, which calls the right
/// type's methods at run time. For an interface Shape it emits, in the gen
/// line's module:
///
/// - `mut view struct ShapeDyn`, with `obj: dyn.Ref`, the object, and `table`,
///   a record with one fn field per method;
/// - `ShapeDyn.Of[T: Shape](x: mut T): ShapeDyn from x`, which holds x's
///   exclusive loan;
/// - `impl Shape for ShapeDyn`, whose methods each load one fn from the
///   table and call it, so a ShapeDyn passes wherever a Shape does.
///
/// Each table field is its method's name in snake_case, with `_` after a
/// keyword or a contextual word such as from or result (ReadAt is
/// read_at, Error is error_). The table holds generic
/// functions named ShapeDyn.Adapt<M>[T], one per method M. When I is
/// exported, ShapeDyn and ShapeDyn.Of are too.
///
/// E0903 at I when I is not an interface or has no method, or when a
/// method takes self by value or has no receiver, has type parameters of
/// its own, declares a flow clause, takes a const parameter, or mentions
/// Self. Also E0903 for a method named Of, Obj or Table, or starting with
/// Adapt, compared as E0206 compares names: the output declares those.
/// Runs at compile time, O(methods and their parameters); allocates the
/// emitted code in the run's arena. Each call through the output costs
/// one key comparison and one indirect call.
export Of(g: mut Gen, i: Type) {
    if i.Kind() != TypeKind.Interface {
        g.FailOn(i, "dyn.Of: needs an interface")
        return
    }
    ms := i.Methods()
    if ms.Len() == 0 {
        g.FailOn(i, "dyn.Of: the interface has no method to forward")
        return
    }
    for m in ms {
        why := Refusal(m)
        if why.Len() > 0 {
            g.FailOn(i, why)
            return
        }
    }
    name := g.Ident(Join(&*g, i.Text(), "Dyn"))
    // table is the record type of the fn fields and init its value; both
    // are built as Code, one `slot: ...` per method joined by commas.
    var table := quote {}
    var init := quote {}
    var adapters := g.Block()
    var forwards := g.Block()
    var first_slot := true
    for m in ms {
        slot := g.Ident(Slot(&*g, m.Name()))
        adapt := g.Ident(Join(&*g, "Adapt", m.Name()))
        method := g.Ident(m.Name())
        result := m.Result()
        is_void := IsVoid(result)
        // o names the adapter's erased-object parameter and f the forwarding
        // method's fn local; each is a name no parameter of m folds to,
        // since both sit beside m's own parameters.
        o := g.Ident(Local(&*g, m, "obj"))
        f := g.Ident(Local(&*g, m, "f"))
        // obj is the adapter's first parameter and recv how it recovers the
        // T; recvp is the forwarding method's receiver and pass the call's
        // arguments, its object first; slot_ty is the fn type's parameters.
        var obj := quote { $o: &dyn.Ref }
        var recv := quote { $o.Get[T]() }
        var recvp := quote { &self }
        var pass := quote { &self.obj }
        var slot_ty := quote { &dyn.Ref }
        if m.Receiver() == Receiver.Mut {
            obj = quote { $o: mut dyn.Ref }
            recv = quote { $o.Mut[T]() }
            recvp = quote { mut self }
            pass = quote { mut self.obj }
            slot_ty = quote { mut dyn.Ref }
        }
        var params := quote {}
        var args := quote {}
        var first := true
        for p in m.Params() {
            params = params.Then(quote { , $(p.Name()): $(p.Type()) })
            slot_ty = slot_ty.Then(quote { , $(p.Type()) })
            pass = pass.Then(quote { , $(p.Name()) })
            args = if first { quote { $(p.Name()) } } else { args.Then(quote { , $(p.Name()) }) }
            first = false
        }
        fn_ty := if is_void { quote { fn($slot_ty) } } else { quote { fn($slot_ty): $result } }
        if first_slot {
            table = quote { $slot: $fn_ty }
            init = quote { $slot: $name.$adapt[T] }
        } else {
            table = table.Then(quote { ,
                    $slot: $fn_ty })
            init = init.Then(quote { ,
                    $slot: $name.$adapt[T] })
        }
        first_slot = false
        // How a body hands back the method's result: nothing for void; for
        // E!void, try and nothing; for E!T, return try; else return.
        give := if is_void {
            quote {}
        } else if result.Kind() != TypeKind.Fallible {
            quote { return }
        } else if IsVoid(result.Elem()) {
            quote { try }
        } else {
            quote { return try }
        }
        res := if is_void { quote {} } else { quote { : $result } }
        adapters.Add(quote {
            $name.$adapt[T: $(i.Name())]($obj $params) $res {
                $give $recv.$method($args)
            }
        })
        forwards.Add(quote {
            $method($recvp $params) $res {
                $f := self.table.$slot
                $give $f($pass)
            }
        })
    }
    // One slot per line past one method: newlines inside ( ) are not
    // statement ends, and the printer indents them.
    if ms.Len() > 1 {
        table = quote {
            $table
        }
        init = quote {
            $init
        }
    }
    if i.Exported() {
        g.Emit(quote {
            export mut view struct $name {
                obj: dyn.Ref
                table: ($table)
            }

            export $name.Of[T: $(i.Name())](x: mut T): $name from x {
                return $name(obj: dyn.Ref.Of(x), table: ($init))
            }

            $adapters

            impl $(i.Name()) for $name {
                $forwards
            }
        })
        return
    }
    g.Emit(quote {
        mut view struct $name {
            obj: dyn.Ref
            table: ($table)
        }

        $name.Of[T: $(i.Name())](x: mut T): $name from x {
            return $name(obj: dyn.Ref.Of(x), table: ($init))
        }

        $adapters

        impl $(i.Name()) for $name {
            $forwards
        }
    })
}

// IsVoid reports whether ty is void.
IsVoid(ty: Type): bool {
    return ty.Kind() == TypeKind.Scalar && ty.Text() == "void"
}

// Refusal is why m cannot be forwarded through an erased object, or "".
Refusal(m: Method): str {
    r := m.Receiver()
    if r == Receiver.Value {
        return "dyn.Of: a method takes self by value, which would move the object out of its owner"
    }
    if r == Receiver.Static {
        return "dyn.Of: a method has no receiver, so there is no object to dispatch on"
    }
    if m.Generic() {
        return "dyn.Of: a method has type parameters, and a fn value needs all of its types"
    }
    if m.HasFlows() {
        return "dyn.Of: a method declares a flow clause, which cannot be stated over the erased object"
    }
    n := m.Name()
    if FoldMatch(n, "of", true) || FoldMatch(n, "obj", true) || FoldMatch(n, "table", true) || FoldMatch(n, "adapt", false) {
        return "dyn.Of: a method's name, ignoring case and underscores, is Of, Obj or Table or starts with Adapt, which the generated type already uses (E0206)"
    }
    for p in m.Params() {
        if p.Const() {
            return "dyn.Of: a method has a const parameter, which a fn value cannot have"
        }
        if HasSelf(p.Type()) {
            return "dyn.Of: a method mentions Self, whose type an erased object does not know"
        }
    }
    if HasSelf(m.Result()) {
        return "dyn.Of: a method mentions Self, whose type an erased object does not know"
    }
    return ""
}

// HasSelf reports whether ty is Self or holds it as an element, a generic
// argument, a record field or a part of a fn type. Self is a Scalar to the
// generator API, spelled Self. The API has no accessor for a fn type's
// parameters and result, so a fn type is searched in its text, which spells
// every part, for the word Self.
HasSelf(ty: Type): bool {
    k := ty.Kind()
    if k == TypeKind.Scalar {
        return ty.Text() == "Self"
    }
    if k == TypeKind.Fn {
        return HasWord(ty.Text(), "Self")
    }
    if k == TypeKind.Ref || k == TypeKind.Slice || k == TypeKind.Array || k == TypeKind.Optional || k == TypeKind.RawPtr || k == TypeKind.Fallible {
        return HasSelf(ty.Elem())
    }
    if k == TypeKind.Record {
        for f in ty.Fields() {
            if HasSelf(f.Type()) {
                return true
            }
        }
        return false
    }
    if k == TypeKind.Struct || k == TypeKind.Enum {
        for a in ty.Args() {
            if HasSelf(a) {
                return true
            }
        }
    }
    return false
}

// FoldMatch reports whether name, ignoring case and underscores (the
// folding of E0206), is want (whole) or begins with it (!whole);
// want is lower case with no underscore. ASCII only: a member name is.
FoldMatch(name: str, want: str, whole: bool): bool {
    b := name.AsBytes()
    w := want.AsBytes()
    var j := 0
    for i in 0..b.Len() {
        c := b[i]
        if c != 95 {
            if j == w.Len() {
                return !whole
            }
            l := if c >= 65 && c <= 90 { c + 32 } else { c }
            if l != w[j] {
                return false
            }
            j += 1
        }
    }
    return j == w.Len()
}

// HasWord reports whether text holds word as a whole identifier: not
// inside a longer one (SelfLike, MySelf and Self_ are not Self). ASCII.
HasWord(text: str, word: str): bool {
    b := text.AsBytes()
    w := word.AsBytes()
    n := b.Len()
    m := w.Len()
    if m == 0 || m > n {
        return false
    }
    for i in 0..n - m + 1 {
        var same := true
        for j in 0..m {
            if b[i + j] != w[j] {
                same = false
                break
            }
        }
        if same && (i == 0 || !IdentByte(b[i - 1])) && (i + m == n || !IdentByte(b[i + m])) {
            return true
        }
    }
    return false
}

// IdentByte reports whether c can be part of an identifier.
IdentByte(c: u8): bool {
    return c == 95 || (c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122)
}

// Slot is the table field of the method name: its snake_case, with an
// underscore after it when that is a keyword or a contextual word (Error
// is error_, Is is is_, Type is type_), which Gen.Ident refuses or a
// reader would take for the word. The escape cannot collide with another
// method's slot: two methods whose names fold to one (E0206) are already
// refused by the interface.
Slot(g: &Gen, name: str): str from g {
    s := Snake(g, name)
    if Reserved(s) {
        return Join(g, s, "_")
    }
    return s
}

// Reserved reports whether w is one of the 39 keywords of the language or
// one of the contextual words from, into, ends, fuel, packed, self, result
// and fields. internal/gen's TestKeywordTableMatchesLexer holds this
// list to the lexer's.
Reserved(w: str): bool {
    for k in ["import", "export", "struct", "enum", "error", "interface", "impl", "type", "const", "var",
        "linear", "view", "gen", "quote", "test", "extern", "if", "else", "for", "in", "match", "return",
        "break", "continue", "defer", "onerr", "fail", "try", "scope", "go", "unsafe", "mut", "fn", "is",
        "requires", "ensures", "invariant", "asm", "become",
        "from", "into", "ends", "fuel", "packed", "self", "result", "fields"] {
        if k == w {
            return true
        }
    }
    return false
}

// Local is a name for a local the generated code declares beside the
// parameters of m (the adapter's erased object, the forwarding method's fn):
// base when no parameter's name folds to it (E0206: case and underscores
// ignored, and a local cannot shadow one), else base followed by the first
// of 2, 3, ... that none folds to. With k parameters at most k
// candidates are taken, so the loop ends by the (k+1)th. base is lower case
// with no underscore.
Local(g: &Gen, m: Method, base: str): str from g {
    ar := g.Arena()
    ps := m.Params()
    var n := 1
    for {
        var t := Text.New(ar)
        t.Push(base).Must()
        if n > 1 {
            PushDec(mut t, n)
        }
        cand := ar.Dup(t.Str()).Must()
        var taken := false
        for p in ps {
            if FoldMatch(p.Name(), cand, true) {
                taken = true
                break
            }
        }
        if !taken {
            return cand
        }
        n += 1
    }
}

// PushDec appends n, which is positive, in decimal.
PushDec(t: mut Text, n: int) {
    if n >= 10 {
        PushDec(t, n / 10)
    }
    t.PushChar(Cast[char](48 + n % 10)).Must()
}

// Join is a followed by b, in the run's arena.
Join(g: &Gen, a: str, b: str): str from g {
    ar := g.Arena()
    var t := Text.New(ar)
    t.Push(a).Must()
    t.Push(b).Must()
    return ar.Dup(t.Str()).Must()
}

// Snake is name in snake_case, in the run's arena: an upper-case ASCII
// letter becomes lower case, after an underscore unless it starts the name
// or follows another upper-case letter that a lower-case one does not
// follow (ReadAt is read_at, HTTPGet is http_get, ID is id).
Snake(g: &Gen, name: str): str from g {
    ar := g.Arena()
    var t := Text.New(ar)
    b := name.AsBytes()
    n := b.Len()
    for i in 0..n {
        c := b[i]
        if c >= 65 && c <= 90 {
            if i > 0 {
                prev_upper := b[i - 1] >= 65 && b[i - 1] <= 90
                next_lower := i + 1 < n && b[i + 1] >= 97 && b[i + 1] <= 122
                if !prev_upper || next_lower {
                    t.PushChar('_').Must()
                }
            }
            t.PushChar(Cast[char](c + 32)).Must()
        } else {
            t.PushChar(Cast[char](c)).Must()
        }
    }
    return ar.Dup(t.Str()).Must()
}
