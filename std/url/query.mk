// Why: HTML forms and many APIs send name=value pairs as
// application/x-www-form-urlencoded, whose decoding rules ('+' as space,
// percent escapes, U+FFFD repair) are easy to get subtly wrong.
//
// What: ParseQuery and FormatQuery, the URL Standard's section 5 parser
// (5.1) and serializer (5.2) with UTF-8, the only encoding std.url takes,
// and their helpers FormDecode, FormText, FormLen and FormPut.
//
// How: both are two passes over their input. The parser copies a sequence
// with no '+' and no '%' as it is, one copy with no decoding, zeroing or
// UTF-8 check (it is a slice of a str cut at ASCII bytes, so it is UTF-8),
// and decodes any other sequence's name and value into one buffer of its
// length, which bounds it (an escape is three bytes and decodes to one, and a
// malformed sequence to one U+FFFD, three bytes, for at least one escape).
// The serializer sizes its output, then writes it.

import std.encoding
import std.rt

/// Splits a query (without its '?') into name and value pairs, as the URL
/// Standard's application/x-www-form-urlencoded parser does. Each
/// non-empty '&'-separated piece splits at its first '='; '+' reads as a
/// space, percent escapes are decoded, and malformed UTF-8 becomes U+FFFD.
/// A piece with no '+' and no '%' is one copy of its bytes, with no
/// decoding or UTF-8 check. The pairs keep their order, and a name may
/// repeat. Cannot fail but for OutOfMemory. O(n); allocates at most n
/// bytes and the list in a, and a malformed value's decoding.
export ParseQuery(a: &Arena, s: str): OutOfMemory!List[(name: str, value: str)] from a {
    var out := List[(name: str, value: str)].New(a)
    b := s.AsBytes()
    var start := 0
    for k in 0..b.Len() + 1 {
        if k < b.Len() && b[k] != b'&' {
            continue
        }
        // Steps 3.1 to 3.3: an empty sequence is skipped; the rest splits at
        // its first '='.
        sequence := b[start..k]
        from := start
        start = k + 1
        if sequence.Len() == 0 {
            continue
        }
        var eq := sequence.Len()
        var plain := true
        for i, c in sequence {
            if c == b'=' && eq == sequence.Len() {
                eq = i
            }
            if c == b'+' || c == b'%' {
                plain = false
            }
        }
        value_from := if eq < sequence.Len() { eq + 1 } else { eq }
        if plain {
            // Nothing to decode: one copy, cut at the '='.
            text := try a.Dup(s[from..k])
            try out.Push((name: text[0..eq], value: text[value_from..]))
            continue
        }
        // Steps 3.4 to 3.6, into one buffer of the sequence's length.
        buf := try a.Slice[u8](sequence.Len())
        name_end := FormDecode(buf, 0, sequence[0..eq])
        value_end := FormDecode(buf, name_end, sequence[value_from..])
        name := try FormText(a, buf[0..name_end])
        value := try FormText(a, buf[name_end..value_end])
        try out.Push((name, value))
    }
    return out
}

// FormDecode writes s with '+' read as a space and percent-decoded at
// out[o..], and returns the offset after it.
FormDecode(out: mut []u8, o: int, s: []u8): int {
    var i := 0
    var at := o
    for i < s.Len() {
        c := s[i]
        if c == b'+' {
            out[at] = b' '
            i += 1
        } else if IsEscape(s, i) {
            out[at] = (HexValue(s[i + 1]) << 4) | HexValue(s[i + 2])
            i += 3
        } else {
            out[at] = c
            i += 1
        }
        at += 1
    }
    return at
}

// FormText is UTF-8 decode without BOM of bytes: a view of them when they
// are valid UTF-8, else a decoding in a with each malformed unit replaced
// by U+FFFD and a leading byte order mark kept, as "without BOM" asks
// (std.encoding's DecodeKeepBom). O(n); allocates nothing for
// valid UTF-8, else the decoding.
FormText(a: &Arena, bytes: []u8): OutOfMemory!str from a, bytes {
    text := rt.StrFrom(bytes) else {
        return try encoding.Utf8.DecodeKeepBom(a, bytes)
    }
    return text
}

// FormKeeps is whether the serializer writes c as it is: an ASCII letter
// or digit, '*', '-', '.' or '_', the bytes outside the
// application/x-www-form-urlencoded percent-encode set.
FormKeeps(c: u8): bool {
    return IsAlpha(c) || IsDigit(c) || c == b'*' || c == b'-' || c == b'.' || c == b'_'
}

// FormLen is the length of s serialized.
FormLen(s: []u8): int {
    var n := 0
    for c in s {
        n += if FormKeeps(c) || c == b' ' { 1 } else { 3 }
    }
    return n
}

// FormPut writes s serialized at out[o..] and returns the offset after it.
FormPut(out: mut []u8, o: int, s: []u8): int {
    digits := UpperHex.AsBytes()
    var at := o
    for c in s {
        if FormKeeps(c) {
            out[at] = c
            at += 1
        } else if c == b' ' {
            out[at] = b'+'
            at += 1
        } else {
            out[at] = b'%'
            out[at + 1] = digits[int(c >> 4)]
            out[at + 2] = digits[int(c & 15)]
            at += 3
        }
    }
    return at
}

/// Joins pairs into a query (without a '?'), as the URL Standard's
/// application/x-www-form-urlencoded serializer does: each name and value
/// percent-encoded, a space written as '+', the two joined by '=' and the
/// pairs by '&'. Cannot fail but for OutOfMemory. O(n); allocates the
/// result in a, at most 3n bytes plus a separator per pair.
export FormatQuery(a: &Arena, pairs: [](name: str, value: str)): OutOfMemory!str from a {
    var n := 0
    for (name, value) in pairs {
        if n > 0 {
            n += 1
        }
        n += FormLen(name.AsBytes()) + 1 + FormLen(value.AsBytes())
    }
    out := try a.Slice[u8](n)
    var o := 0
    for (name, value) in pairs {
        if o > 0 {
            out[o] = b'&'
            o += 1
        }
        o = FormPut(mut out[..], o, name.AsBytes())
        out[o] = b'='
        o = FormPut(mut out[..], o + 1, value.AsBytes())
    }
    // Must: every byte written is ASCII.
    return rt.StrFrom(out[..]).Must()
}
