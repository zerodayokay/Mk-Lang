// Why: a capturing function literal reaches its captures through a hidden
// environment parameter, and a loan escaping through or stored into it needs
// its own error, one that names the captured variable rather than a parameter
// the user never wrote.
//
// What: hiddenParam, envFields, captureName, captureTargetName and
// noteCapField (naming a capture), loanCapture and pathCapture,
// captureE0609, hasInnerSharedDeref and captureBackstop.
//
// How: the hidden parameter counts as a parameter for liveness and
// initialization but is no declared source, so a result loan rooted at it, a
// loan rooted at it stored into a declared parameter, or a non-static loan
// stored into it is E0609 instead of E0602 or E0604. A write, a mut borrow or
// a move of a capture's own place that check missed is E0609 instead of E0507
// or E0409 (the backstop), while a write through a captured &T or []T stays
// E0507. Every form is reported in the flow group.

package borrow

import (
	"strings"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// hiddenParam reports whether parameter p is a hidden environment parameter:
// past the signature's declared parameters.
func (c *checker) hiddenParam(p types.ParamIndex) bool {
	return c.env.Sig != nil && p != types.NoParam && int(p) > len(c.env.Sig.Params) && int(p) <= int(c.body.ParamCount)
}

// envFields are the environment record's fields of hidden parameter p.
func (c *checker) envFields(p types.ParamIndex) []types.RecordField {
	if c.tab == nil || int(p) >= len(c.body.Locals) {
		return nil
	}
	t := c.tab.Get(c.body.Locals[p].Type)
	if t.Kind != types.KRef && t.Kind != types.KMut {
		return nil
	}
	r := c.tab.Get(t.Elem)
	if r.Kind != types.KRecord {
		return nil
	}
	return r.Fields
}

// captureName is the captured name field i of p's environment stands for;
// every name when i is out of range (a loan on the whole record).
func (c *checker) captureName(p types.ParamIndex, i int, known bool) string {
	fs := c.envFields(p)
	if known && i >= 0 && i < len(fs) {
		return c.symbol(fs[i].Name)
	}
	var names []string
	for _, f := range fs {
		names = append(names, c.symbol(f.Name))
	}
	return strings.Join(names, "`, `")
}

// loanCapture reports whether loan l is a hidden parameter's abstract loan,
// and the captured name it reaches (its tag's first field, the record's).
func (c *checker) loanCapture(l loanID) (string, bool) {
	lo := &c.loans[l]
	if !lo.abstract || !c.hiddenParam(lo.param) {
		return "", false
	}
	if lo.inner && len(lo.tag) > 0 {
		return c.captureName(lo.param, int(lo.tag[0]), true), true
	}
	return c.captureName(lo.param, 0, false), true
}

// pathCapture reports whether path p is rooted at a hidden environment
// parameter (the local itself or its abstract pointee), and the captured
// name: the record field the path selects.
func (c *checker) pathCapture(p pathID) (string, bool) {
	pi := c.paths.get(p)
	ri := &c.roots[pi.root]
	if (ri.kind != rkParam && ri.kind != rkAbstract) || !c.hiddenParam(ri.param) {
		return "", false
	}
	for _, s := range pi.steps {
		if s.kind == stField {
			return c.captureName(ri.param, int(s.a), true), true
		}
	}
	return c.captureName(ri.param, 0, false), true
}

// captureTargetName is the capture name reached at abstract root t of hidden
// parameter p during the current write or call store, falling back to all
// capture names of p when more than one or no specific field is known.
func (c *checker) captureTargetName(p types.ParamIndex, t rootID) string {
	if i, ok := c.capField[t]; ok && i >= 0 {
		return c.captureName(p, i, true)
	}
	return c.captureName(p, 0, false)
}

// noteCapField records which environment field of a hidden parameter loan l
// reaches abstract root t through, or -1 when unknown or more than one field
// reaches t.
func (c *checker) noteCapField(t rootID, l loanID) {
	if !c.report || c.capField == nil {
		return
	}
	ri := &c.roots[t]
	if ri.kind != rkAbstract || !c.hiddenParam(ri.param) {
		return
	}
	f := -1
	if lo := &c.loans[l]; lo.abstract && lo.param == ri.param && lo.inner && len(lo.tag) > 0 {
		f = int(lo.tag[0])
	}
	c.setCapField(t, f)
}

func (c *checker) setCapField(t rootID, f int) {
	if !c.report || c.capField == nil {
		return
	}
	ri := &c.roots[t]
	if ri.kind != rkAbstract || !c.hiddenParam(ri.param) {
		return
	}
	if cur, ok := c.capField[t]; !ok {
		c.capField[t] = f
	} else if cur != f {
		c.capField[t] = -1
	}
}

// hasInnerSharedDeref reports whether path p (rooted at a hidden environment
// parameter) crosses a shared dereference inside the captured value itself,
// past the environment wrapper (*env).x (and *((*env).x) for a &T field).
// Writes and mut borrows across such a step are normal E0507 on the captured
// reference or slice (e.g. *p = 1 on captured p: &i32), not E0609.
func (c *checker) hasInnerSharedDeref(p pathID) bool {
	pi := c.paths.get(p)
	ri := &c.roots[pi.root]
	if ri.kind != rkParam || !c.hiddenParam(ri.param) {
		return false
	}
	fs := c.envFields(ri.param)
	start := 1
	for i, st := range pi.steps {
		if st.kind == stField && int(st.a) < len(fs) {
			start = i + 1
			if c.tab.Kind(fs[st.a].Type) == types.KRef && start < len(pi.steps) && pi.steps[start].kind == stDeref {
				start++
			}
			break
		}
	}
	for _, st := range pi.steps[start:] {
		if st.kind == stDeref && st.mode == dmShared {
			return true
		}
	}
	return false
}

// callbackRecipe is check.CallbackRecipe (borrow does not import check): the
// sentence every E0609 message ends with.
const callbackRecipe = "; to store a callback, store a `fn` value and its plain state; see the cookbook's stored-callback recipe"

// captureE0609 emits E0609 in the flow group with the captured field and
// reason as facts, and the rewrite the message names.
func (c *checker) captureE0609(sp mir.SpanID, name, reason, msg string) *diag.Diagnostic {
	d := c.errAt(diag.E0609, sp, "%s", msg+callbackRecipe).Fact("captured", diag.TextList(name)).Text("reason", reason).In(diag.PhaseFlow)
	d.Alt("a struct holding what the callback needs, and a named `fn` that takes it").Alt("a loop instead of the callback")
	switch reason {
	case "write", "mut-borrow", "mut-self":
		d.Alt("pass the state `mut` through an explicit parameter")
	}
	return d
}

// captureBackstop is the backstop: E0609 for a write, mut borrow or move of a place
// rooted at a hidden environment parameter, which check's table did not
// report; true when it reported, so the caller skips its own code.
func (c *checker) captureBackstop(ctx *stmtCtx, p pathID, reason string) bool {
	name, ok := c.pathCapture(p)
	if !ok {
		return false
	}
	if reason != "move" && c.hasInnerSharedDeref(p) {
		return false
	}
	if c.report {
		var msg string
		switch reason {
		case "write":
			msg = "the literal assigns `" + name + "`, which it captured; a capture is read through a shared reference"
		case "move":
			msg = "the literal moves `" + name + "`, which it captured; a capture is borrowed and cannot be moved out of, and only a plain one is copied"
		default:
			msg = "the literal borrows `" + name + "` `mut`, which it captured; a capture is read through a shared reference"
		}
		c.captureE0609(ctx.span, name, reason, msg)
	}
	return true
}
