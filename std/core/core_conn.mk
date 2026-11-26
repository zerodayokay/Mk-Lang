// Why: std.net's connection and connector types hide what transport or TLS
// layer sits behind them, which needs type erasure; keeping the unsafe part
// here lets the modules above std.core erase values with safe calls only.
//
// What: ErasedBox (Of, Fill, Ref, Take, Is), a boxed Send + Share value with
// its type erased, ErasedCall (Of, State, Fn), a function and the shared
// state it is called with, and TypedCall, an ErasedCall that keeps its
// function's type.
//
// How: an erased value keeps its type's key (std.dyn.erased.Key, a
// compile-time constant distinct for distinct types) and is given back only
// to a caller that names the same type; any other type panics with code 8
// before a byte is read. A box is given back only as &T with T: Send + Share,
// so every operation on it is shared and no Writer, &Arena, Cell or other
// task-bound value can sit behind it. Nothing here allocates or takes an Io:
// the caller boxes the value on its &Heap with Box[T].New and frees it after
// Take.

import std.raw
import std.dyn.erased

/// A boxed Send + Share value whose type is erased, such as the state of a
/// connection. It keeps the Box's allocator and loans. The value comes back
/// only to a caller that names the type it was made from: as &T through Ref,
/// or as the Box itself through Take, to free it. Linear: Take ends it.
export linear mut view struct ErasedBox {
    ptr: *u8
    owner: *Alloc
    key: u64
}

// Of takes a Box[T] with T: Send + Share and only Ref and Take reach the
// value, each as the T named at Of (the key test): what a Box[T] of such a
// T may do across tasks, an ErasedBox does.
unsafe impl Send for ErasedBox {}
unsafe impl Share for ErasedBox {}

/// Erases b's type. The ErasedBox owns the value and its memory, and keeps
/// b's allocator and loans. O(1); allocates nothing.
export ErasedBox.Of[T: Send + Share](b: Box[T]): ErasedBox from b {
    Box(ptr, owner, marker) := b
    unsafe "a zero-length marker holds no value; a pointer cast of the box's own address" {
        raw.Forget(marker)
        return ErasedBox(ptr: raw.PtrCast[T, u8](ptr), owner: owner, key: erased.Key[T]())
    }
}

/// Moves x into b, which must hold none, and erases the box's type as Of
/// does. The ErasedBox is made from a ?T holding some(x), so Ref[?T] and
/// Take[?T] give it back. b is consumed, so the result holds the loans of
/// both b and x, and nothing the caller keeps receives x. This lets a box
/// reserved on a process-wide heap take a value that borrows a local, which
/// a store through b.Mut() would refuse. Panics (code 8) when b holds some,
/// and then b's value, x and the memory leak. O(1); allocates nothing.
export ErasedBox.Fill[T: Send + Share](b: Box[?T], x: T): ErasedBox from b, x {
    Box(ptr, owner, marker) := b
    unsafe "ptr is the address of b's live ?T, which b owned and this call now does: it is read only for its tag, then x moves in once it holds none, so no value is dropped or written twice; only the result reaches that memory and it is from b and x, so the store needs no into clause; a zero-length marker holds no value, and the pointer cast is of the box's own address" {
        if (&*ptr).IsSome() {
            raw.Fail(8, "ErasedBox.Fill: the box is not empty")
        }
        raw.Write(ptr, some(x))
        raw.Forget(marker)
        return ErasedBox(ptr: raw.PtrCast[?T, u8](ptr), owner: owner, key: erased.Key[?T]())
    }
}

/// The boxed value, shared: the only access to it while it is erased.
/// Panics (code 8) when T is not the type the box was made from. O(1).
export ErasedBox.Ref[T: Send + Share](&self): &T from self {
    if self.key != erased.Key[T]() {
        ErasedMismatch()
    }
    unsafe "Of stored the address of a live T that this ErasedBox owns, and the key says the type is T; only & is given, and T is Share" {
        return &*raw.PtrCast[u8, T](self.ptr)
    }
}

/// The Box the value was erased from, so its owner can move the value out
/// or free it with the allocator it was made with (another allocator panics,
/// code 12). Panics (code 8) when T is not the type the box was made from,
/// and then the value and its memory leak. O(1); allocates nothing.
export ErasedBox.Take[T: Send + Share](self): Box[T] from self {
    ErasedBox(ptr, owner, key) := self
    if key != erased.Key[T]() {
        ErasedMismatch()
    }
    unsafe "Of took ptr and owner apart from a Box[T], and the key says the type is T: this puts the same Box back together" {
        return Box[T](ptr: raw.PtrCast[u8, T](ptr), owner: owner, marker: [])
    }
}

/// Whether the box was made from a T. O(1).
export ErasedBox.Is[T](&self): bool {
    return self.key == erased.Key[T]()
}

/// A boxed Send + Share value whose type is erased and whose access is
/// mediated solely by zero-flow callbacks (`With` and `TakeWith`), so no
/// caller can store a loan into it or borrow its contents out. Linear:
/// TakeWith ends it.
export linear view struct SealedBox {
    ptr: *u8
    owner: *Alloc
    key: u64
}

unsafe impl Send for SealedBox {}
unsafe impl Share for SealedBox {}

/// Moves x into b, which must hold none, and erases the box's type as
/// ErasedBox.Fill does. Linear: TakeWith ends it. O(1); allocates nothing.
export SealedBox.Fill[T: Send + Share](b: Box[?T], x: T): SealedBox from b, x {
    Box(ptr, owner, marker) := b
    unsafe "ptr is the address of b's live ?T, moved in once it holds none; only the result reaches that memory and it holds b's and x's loans" {
        if (&*ptr).IsSome() {
            raw.Fail(8, "SealedBox.Fill: the box is not empty")
        }
        raw.Write(ptr, some(x))
        raw.Forget(marker)
        return SealedBox(ptr: raw.PtrCast[?T, u8](ptr), owner: owner, key: erased.Key[?T]())
    }
}

/// Calls f with a shared reference to the boxed value and argument a.
/// Because f's type declares no `into` flow and returns void, f can neither
/// store any non-static loan into the boxed value nor carry a loan out of it.
/// Panics (code 8) when T is not the type the box was made from. O(1).
export SealedBox.With[T: Send + Share, A](&self, a: A, f: fn(&T, A): void) {
    if self.key != erased.Key[T]() {
        ErasedMismatch()
    }
    unsafe "Fill stored the address of a live T that this SealedBox owns, and the key says the type is T; only & is given, and T is Share" {
        f(&*raw.PtrCast[u8, T](self.ptr), a)
    }
}

/// Calls f with a shared reference to the boxed value, flow source h (which
/// f may store into *out), argument a and mutable destination out. Returns
/// void so no loan of self or a escapes. Panics (code 8) when T mismatches.
/// O(1).
export SealedBox.WithInto[T: Send + Share, H, A, R](&self, h: H into out, a: A, out: mut R, f: fn(&T, h: H into out, a: A, out: mut R): void) {
    if self.key != erased.Key[T]() {
        ErasedMismatch()
    }
    unsafe "Fill stored the address of a live T that this SealedBox owns, and the key says the type is T; f may only store h into *out so no loan of self escapes" {
        f(&*raw.PtrCast[u8, T](self.ptr), h, a, out)
    }
}

/// Consumes the SealedBox and calls f with the recovered Box and argument a.
/// Because f's type declares no `into` flow and returns void, f can neither
/// store any non-static loan into the boxed value nor carry a loan out of it.
/// Panics (code 8) when T is not the type the box was made from. O(1);
/// allocates nothing.
export SealedBox.TakeWith[T: Send + Share, A](self, a: A, f: fn(Box[T], A): void) {
    SealedBox(ptr, owner, key) := self
    if key != erased.Key[T]() {
        ErasedMismatch()
    }
    unsafe "Fill took ptr and owner apart from a Box[T], and the key says the type is T: this puts the same Box back together and hands it to f" {
        f(Box[T](ptr: raw.PtrCast[u8, T](ptr), owner: owner, marker: []), a)
    }
}

/// A function and the shared state it is called with, with the state's type
/// erased, such as a connector of std.net (net.Secure). Plain, Send and
/// Share, so an options record can hold it: a copy holds the same loans, and
/// only &T with T: Share comes back. Every operation is O(1) and allocates
/// nothing.
export struct ErasedCall {
    /// Holds the loans Of gave the value. It points at no data and nothing
    /// reads it; it is exported because a field that carries loans in a
    /// plain exported struct must be (E0410).
    export anchor: &[0]u8
    addr: uint
    code: uint
    key: u64
}

// CallKey is the type an ErasedCall's key is taken from: the pair of the
// state's type and the function's.
struct CallKey[T, F] {
    t: [0]&T
    f: [0]F
}

/// Erases the types of state and f. f is a fn whose first parameter is &T,
/// and F is Send and Share as the ErasedCall is, so a reference to a Cell is
/// refused. The ErasedCall holds the loans of state and f (a fn value has
/// none). Panics (code 8) unless F is one word with a word's alignment, as a
/// fn value is: an [8]u8 or a record of two u32 is refused. O(1); allocates
/// nothing.
export ErasedCall.Of[T: Share, F: Plain + Send + Share](state: &T, f: F): ErasedCall from state, f {
    if SizeOf[F]() != SizeOf[uint]() || AlignOf[F]() != AlignOf[uint]() {
        raw.Fail(8, "ErasedCall.Of: not a function value")
    }
    // A type of one word's size and alignment has a field of that
    // alignment, so of that size: it fills the word, with no padding.
    unsafe "F is plain, one word in size and aligned as uint is (checked above), so &f is aligned for a uint load and every byte of it is F's: a copy of its bits" {
        code := raw.Read(raw.PtrCast[F, uint](raw.Of(&f)))
        return ErasedCall(anchor: &NoBytes, addr: raw.Addr(raw.Of(state)), code: code, key: erased.Key[CallKey[T, F]]())
    }
}

/// The state. Panics (code 8) when T and F are not the types the ErasedCall
/// was made from. O(1). The result borrows the state itself, not the
/// ErasedCall, so a write through it, such as through a guard of a Mutex
/// state, is a write into the state and not into the ErasedCall.
export ErasedCall.State[T: Share, F: Plain + Send + Share](&self): &T from self.anchor {
    if self.key != erased.Key[CallKey[T, F]]() {
        ErasedMismatch()
    }
    unsafe "Of stored the address of a T under the shared loan this ErasedCall holds (its fields but anchor are std.core's, so no other module made it), and the key says the type is T" {
        return &*raw.FromAddr[T](self.addr)
    }
}

/// The function. Panics (code 8) when T and F are not the types the
/// ErasedCall was made from. O(1).
export ErasedCall.Fn[T: Share, F: Plain + Send + Share](&self): F from self {
    if self.key != erased.Key[CallKey[T, F]]() {
        ErasedMismatch()
    }
    unsafe "Of stored the bits of an F (its fields but anchor are std.core's, so no other module made it), and the key says the type is F, whose alignment Of checked is uint's, so &self.code is aligned for it: a copy of them back" {
        return raw.Read(raw.PtrCast[uint, F](raw.Of(&self.code)))
    }
}

/// An ErasedCall that keeps its function's type F in its own type, so a
/// function that takes a TypedCall of one F refuses, at compile time, a
/// call made from a function of another type (an ErasedCall of it would
/// panic only when recovered). F names the state's type too, as its
/// first parameter's. Plain, Send and Share, as ErasedCall is.
export struct TypedCall[F: Plain + Send + Share] {
    /// The erased call, made by ErasedCall.Of from the state's type and F;
    /// it holds the loans Of gave it.
    export call: ErasedCall
    f: [0]F
}

/// The call of f on state, as ErasedCall.Of[T, F] makes it, typed by F.
/// Panics as ErasedCall.Of does. O(1); allocates nothing.
export TypedCall[F].Of[T: Share](state: &T, f: F): TypedCall[F] from state, f {
    return TypedCall[F](call: ErasedCall.Of[T, F](state, f), f: [])
}

const NoBytes: [0]u8 = []
// ErasedMismatch is the panic of a recovery under another type: only a std
// module that pairs an erased value with another type's function reaches
// it, which is a bug in that module.
ErasedMismatch(): never {
    raw.Fail(8, "an erased value recovered as a type it was not made from")
}
