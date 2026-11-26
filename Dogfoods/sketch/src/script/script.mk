// Why: a script drives the program the way a person would, so the same
// steps can run against the document alone on any target, and against a
// real window on Windows, and must give the same picture.
//
// What: Step, StepKind, Script, Error, BadScript and Parse.
//
// How: one step per line, `#` comments; numbers are checked here, so a run
// never meets a malformed step. The first step is `canvas W H`.

import src.doc

/// The step kinds; see Dogfoods/sketch/README.md for what each does.
export enum StepKind {
    Color(r: u8, g: u8, b: u8)
    Brush(radius: int)
    Move(x: int, y: int)
    Down
    Up
    Key(name: KeyName)
    Text(s: str)
    Resize(width: int, height: int)
    Minimize
    Restore
    Focus(on: bool)
    Cursor(name: CursorName)
    Fullscreen(on: bool)
    Save(path: str)
    Load(path: str)
    Snapshot(path: str)
    ExpectCounts(strokes: int, undone: int)
    ExpectHash(hex: str)
    ExpectHeld(left: bool)
    ExpectCaption(text: str)
    ExpectSaved(path: str)
    Print
    Close
}

/// The keys a script may press.
export enum KeyName {
    Undo
    Redo
    Clear
    Bigger
    Smaller
    Type
    Escape
    Backspace
    Palette(index: int)
}

/// The cursors a script may choose; Hidden hides it.
export enum CursorName {
    Arrow
    Crosshair
    Hand
    Text
    Hidden
}

/// One step and the line it came from.
export struct Step {
    export line: int
    export kind: StepKind
}

/// A parsed script: the canvas size and the steps after it.
export struct Script {
    export width: int
    export height: int
    export steps: List[Step]
}

/// Errors Parse returns.
export open type Error = BadScript

/// The script is malformed at line (1-based); msg says how.
export error BadScript(line: int, msg: str)

/// The most steps a script holds.
export const MaxSteps: int = 100000

// Num is the next word as an integer in lo..hi.
Num(w: mut Words, lo: int, hi: int): ?int {
    s := w.Next() else { return none }
    v := s.ParseInt() else { return none }
    if v < i64(lo) || v > i64(hi) {
        return none
    }
    return some(int(v))
}

OnOff(w: mut Words, line: int): Error!bool {
    s := w.Next() else { fail BadScript(line: line, msg: "expected on or off") }
    if s.Eq(&"on") {
        return true
    }
    if s.Eq(&"off") {
        return false
    }
    fail BadScript(line: line, msg: "expected on or off")
}

Rest(l: str, head: str): str from l {
    return l[head.Len()..l.Len()].Trim()
}

ParseKey(s: str, line: int): Error!KeyName {
    if s.Eq(&"undo") { return KeyName.Undo }
    if s.Eq(&"redo") { return KeyName.Redo }
    if s.Eq(&"clear") { return KeyName.Clear }
    if s.Eq(&"bigger") { return KeyName.Bigger }
    if s.Eq(&"smaller") { return KeyName.Smaller }
    if s.Eq(&"type") { return KeyName.Type }
    if s.Eq(&"escape") { return KeyName.Escape }
    if s.Eq(&"backspace") { return KeyName.Backspace }
    if s.Len() == 1 && s.Byte(0) >= 49 && s.Byte(0) <= 56 {
        return KeyName.Palette(index: int(s.Byte(0)) - 49)
    }
    fail BadScript(line: line, msg: "unknown key")
}

ParseCursor(s: str, line: int): Error!CursorName {
    if s.Eq(&"arrow") { return CursorName.Arrow }
    if s.Eq(&"crosshair") { return CursorName.Crosshair }
    if s.Eq(&"hand") { return CursorName.Hand }
    if s.Eq(&"text") { return CursorName.Text }
    if s.Eq(&"hidden") { return CursorName.Hidden }
    fail BadScript(line: line, msg: "unknown cursor")
}

// PathArg is the rest of the line after head: a file name with no spaces,
// relative to the working directory.
PathArg(l: str, head: str, line: int): Error!str from l {
    p := Rest(l, head)
    if p.Len() == 0 || p.Find(" ").IsSome() {
        fail BadScript(line: line, msg: "expected one file name")
    }
    return p
}

/// Parses script text; the steps borrow text. Errors: Error (BadScript),
/// OutOfMemory. O(text); allocates the step list in a.
export Parse(a: &Arena, text: str into a): Error|OutOfMemory!Script from a, text {
    var sc := Script(width: 0, height: 0, steps: List[Step].New(a))
    var line_no := 0
    for raw in text.Lines() {
        line_no += 1
        cut := match raw.Find("#") {
            some(k) { raw[0..k] }
            none { raw }
        }
        l := cut.Trim()
        if l.Len() == 0 {
            continue
        }
        var w := l.Words()
        head := w.Next() else { continue }
        if sc.width == 0 {
            if !head.Eq(&"canvas") {
                fail BadScript(line: line_no, msg: "the first step is canvas W H")
            }
            sc.width = Num(mut w, 16, doc.MaxSide) else { fail BadScript(line: line_no, msg: "a canvas side is 16..2048") }
            sc.height = Num(mut w, 16, doc.MaxSide) else { fail BadScript(line: line_no, msg: "a canvas side is 16..2048") }
            continue
        }
        if sc.steps.Len() >= MaxSteps {
            fail BadScript(line: line_no, msg: "too many steps")
        }
        side := doc.MaxSide
        kind := if head.Eq(&"color") {
            r := Num(mut w, 0, 255) else { fail BadScript(line: line_no, msg: "a colour is 0..255") }
            g := Num(mut w, 0, 255) else { fail BadScript(line: line_no, msg: "a colour is 0..255") }
            b := Num(mut w, 0, 255) else { fail BadScript(line: line_no, msg: "a colour is 0..255") }
            StepKind.Color(r: Trunc[u8](r), g: Trunc[u8](g), b: Trunc[u8](b))
        } else if head.Eq(&"brush") {
            StepKind.Brush(radius: Num(mut w, 1, doc.MaxRadius) else { fail BadScript(line: line_no, msg: "a radius is 1..64") })
        } else if head.Eq(&"move") {
            x := Num(mut w, 0, sc.width - 1) else { fail BadScript(line: line_no, msg: "a point lies on the canvas") }
            y := Num(mut w, 0, sc.height - 1) else { fail BadScript(line: line_no, msg: "a point lies on the canvas") }
            StepKind.Move(x: x, y: y)
        } else if head.Eq(&"down") {
            StepKind.Down
        } else if head.Eq(&"up") {
            StepKind.Up
        } else if head.Eq(&"key") {
            k := w.Next() else { fail BadScript(line: line_no, msg: "key NAME") }
            StepKind.Key(name: try ParseKey(k, line_no))
        } else if head.Eq(&"text") {
            t := Rest(l, head)
            if t.Len() == 0 {
                fail BadScript(line: line_no, msg: "text TEXT")
            }
            StepKind.Text(s: t)
        } else if head.Eq(&"resize") {
            rw := Num(mut w, 16, side) else { fail BadScript(line: line_no, msg: "a window side is 16..2048") }
            rh := Num(mut w, 16, side) else { fail BadScript(line: line_no, msg: "a window side is 16..2048") }
            StepKind.Resize(width: rw, height: rh)
        } else if head.Eq(&"minimize") {
            StepKind.Minimize
        } else if head.Eq(&"restore") {
            StepKind.Restore
        } else if head.Eq(&"focus") {
            StepKind.Focus(on: try OnOff(mut w, line_no))
        } else if head.Eq(&"cursor") {
            c := w.Next() else { fail BadScript(line: line_no, msg: "cursor NAME") }
            StepKind.Cursor(name: try ParseCursor(c, line_no))
        } else if head.Eq(&"fullscreen") {
            StepKind.Fullscreen(on: try OnOff(mut w, line_no))
        } else if head.Eq(&"save") {
            StepKind.Save(path: try PathArg(l, head, line_no))
        } else if head.Eq(&"load") {
            StepKind.Load(path: try PathArg(l, head, line_no))
        } else if head.Eq(&"snapshot") {
            StepKind.Snapshot(path: try PathArg(l, head, line_no))
        } else if head.Eq(&"expect") {
            what := w.Next() else { fail BadScript(line: line_no, msg: "expect WHAT") }
            if what.Eq(&"strokes") {
                n := Num(mut w, 0, doc.MaxStrokes) else { fail BadScript(line: line_no, msg: "expect strokes N undone M") }
                u := w.Next() else { fail BadScript(line: line_no, msg: "expect strokes N undone M") }
                if !u.Eq(&"undone") {
                    fail BadScript(line: line_no, msg: "expect strokes N undone M")
                }
                m := Num(mut w, 0, doc.MaxStrokes) else { fail BadScript(line: line_no, msg: "expect strokes N undone M") }
                StepKind.ExpectCounts(strokes: n, undone: m)
            } else if what.Eq(&"hash") {
                hx := w.Next() else { fail BadScript(line: line_no, msg: "expect hash HEX") }
                if hx.Len() != 64 {
                    fail BadScript(line: line_no, msg: "a hash is 64 hex digits")
                }
                StepKind.ExpectHash(hex: hx)
            } else if what.Eq(&"held") {
                h := w.Next() else { fail BadScript(line: line_no, msg: "expect held left|none") }
                if !h.Eq(&"left") && !h.Eq(&"none") {
                    fail BadScript(line: line_no, msg: "expect held left|none")
                }
                StepKind.ExpectHeld(left: h.Eq(&"left"))
            } else if what.Eq(&"saved") {
                StepKind.ExpectSaved(path: try PathArg(Rest(l, head), what, line_no))
            } else if what.Eq(&"caption") {
                StepKind.ExpectCaption(text: Rest(Rest(l, head), what))
            } else {
                fail BadScript(line: line_no, msg: "expect strokes|hash|held|caption|saved")
            }
        } else if head.Eq(&"print") {
            StepKind.Print
        } else if head.Eq(&"close") {
            StepKind.Close
        } else {
            fail BadScript(line: line_no, msg: "unknown step")
        }
        try sc.steps.Push(Step(line: line_no, kind: kind))
    }
    if sc.width == 0 {
        fail BadScript(line: line_no + 1, msg: "the first step is canvas W H")
    }
    return sc
}
