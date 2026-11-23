// Why: generated dispatch code lives in the user's module and may call
// anything std.dyn exports, so the erased handle must be sound on its own.
//
// What: Ref, the erased handle gen dyn.Of's output holds, with Ref.Of,
// Ref.Mut, Ref.Get and Ref.Is.
//
// How: a Ref is made from one exclusive loan of a T, carries T's key, and
// gives the T back only to a caller that names the same T. It lives in
// std.dyn, not a module of its own, so dyn.mk's quotes name it as `dyn.Ref`:
// that name survives a cache load of this module, where an import binding
// does not. Rust's `dyn` needs no such key because its vtables are
// compiler-sealed, which a generated table in user code is not. Imports
// std.raw and std.dyn.erased only, allocates nothing, no Io.

import std.raw
import std.dyn.erased

/// Ref is an object whose type is erased: the object's address and its
/// type's key. It holds the exclusive loan of the object it was made from,
/// and as a view it is unique: it cannot be copied.
export mut view struct Ref {
    p: *u8
    key: u64
}

/// Of erases x's type: the Ref holds x's exclusive loan, so x cannot be
/// used another way until the Ref's last use. O(1); allocates nothing.
export Ref.Of[T](x: mut T): Ref from x {
    unsafe "a pointer cast only; Mut and Get cast back under the key test" {
        return Ref(p: raw.PtrCast[T, u8](raw.OfMut(x)), key: erased.Key[T]())
    }
}

/// Mut gives the object back, writable, as the T it was made from. It
/// panics (code 8) when the Ref was made from another type. Code that
/// gen dyn.Of writes never does that; only code that pairs a Ref with the
/// wrong type by hand can. O(1).
export Ref.Mut[T](mut self): mut T from self {
    if self.key != erased.Key[T]() {
        Mismatch()
    }
    unsafe "Of stored the address of a live T under an exclusive loan this Ref holds, and the key says the type is T" {
        return mut *raw.PtrCast[u8, T](self.p)
    }
}

/// Get gives the object back for reading, as the T it was made from. It
/// panics (code 8), as Mut does, when the Ref was made from another type.
/// O(1).
export Ref.Get[T](&self): &T from self {
    if self.key != erased.Key[T]() {
        Mismatch()
    }
    unsafe "Of stored the address of a live T under an exclusive loan this Ref holds, and the key says the type is T" {
        return &*raw.PtrCast[u8, T](self.p)
    }
}

/// Is reports whether the Ref was made from a T, so a caller can test
/// before Get or Mut. O(1).
export Ref.Is[T](&self): bool {
    return self.key == erased.Key[T]()
}

Mismatch(): never {
    raw.Fail(8, "dyn: an erased object recovered as a type it was not made from")
}
