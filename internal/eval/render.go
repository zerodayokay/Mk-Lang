// Why: an evaluation failure reaches the user from several places (gen
// lines, const declarations, check); one renderer keeps their message,
// facts and fixes the same.
//
// What: Render, which turns an EvalError into E0901 (a panic or a refused
// operation), E0902 (out of fuel), E0908 (a same-module item entered) or
// E0909 (a const cycle), and DataFileDiag (E0910).
//
// How: a switch over the error kind builds the diagnostic with its facts
// and alternatives. The trace is innermost first for E0901 and E0902 and
// outermost first for E0908.

package eval

import (
	"strconv"
	"strings"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// Render renders an EvalError as its diagnostic at req.Site. gen calls it at
// gen lines and the driver at const declarations; check, which cannot import
// eval, implements the same table, and a driver test compares the two
// (TestEvalRenderAgreesWithCheck).
//
// A trace frame is the object {"name", "at"}: the item's name and the
// location the frame stopped or called inward at (a NoItem marker of elided
// frames is {"name": "..."}).
func Render(err *types.EvalError, req *types.EvalRequest, items *types.Items, in *source.Interner) *diag.Diagnostic {
	name := func(it types.ItemID) string {
		if it == types.NoItem {
			return "..."
		}
		return in.Str(items.Get(it).Name)
	}
	frame := func(i int) diag.Fact {
		if err.Items[i] == types.NoItem {
			return diag.ObjectFact(diag.Field{Key: "name", Value: diag.TextFact("...")})
		}
		return diag.ObjectFact(diag.Field{Key: "name", Value: diag.TextFact(name(err.Items[i]))},
			diag.Field{Key: "at", Value: diag.AtFact(err.Trace[i])})
	}
	innermost := func() diag.Fact {
		var fs []diag.Fact
		for i := len(err.Items) - 1; i >= 0; i-- {
			fs = append(fs, frame(i))
		}
		return diag.ListFact(fs...)
	}
	outermost := func() diag.Fact {
		var fs []diag.Fact
		for i := range err.Items {
			fs = append(fs, frame(i))
		}
		return diag.ListFact(fs...)
	}
	site := req.Site
	switch err.Kind {
	case types.EvalPanic:
		msg := "compile-time evaluation panicked: " + err.Panic.String()
		if err.Message != "" {
			msg += ": " + err.Message
		}
		d := diag.New(diag.E0901, site, msg).Text("panic", err.Panic.String())
		switch err.Panic {
		case types.PanicExplicit, types.PanicMust, types.PanicContract:
			d.Text("message", err.Message)
		}
		return d.Fact("trace", innermost())
	case types.EvalRefused:
		return diag.New(diag.E0901, site, "compile-time evaluation refused an operation: "+err.Refusal.String()).
			Text("refused", err.Refusal.String()).Fact("trace", innermost())
	case types.EvalFuel:
		// The limit that ran out: the segment's own, which a const's or
		// global's large value may raise (err.Fuel); else a const's own
		// fuel, never inherited, else the request's (slots.go constFuel).
		fuel := err.Fuel
		if fuel == 0 {
			fuel = req.Fuel
			if d, ok := items.Get(req.Item).Decl.(*types.ConstDecl); ok && req.What == types.EvalConst && d.Fuel != 0 {
				fuel = d.Fuel
			}
		}
		if fuel == 0 {
			fuel = types.DefaultFuel
		}
		d := diag.New(diag.E0902, site, "compile-time evaluation ran out of fuel after "+strconv.FormatUint(err.Steps, 10)+" steps").
			Int("steps", int64(err.Steps)).Int("fuel", int64(fuel)).Fact("trace", innermost())
		// The raised fuel is attached as a checked fix only
		// after a re-run on the error path, which the reporter does; here it
		// is an alternative.
		_, global := items.Get(req.Item).Decl.(*types.GlobalDecl)
		switch {
		case req.What == types.EvalConst && global:
			// A var cannot state fuel (E0101): its work moves to a const.
			d.Alt("a global cannot state fuel: do the work in a const with fuel " + strconv.FormatUint(2*fuel, 10) + ", and initialize " + name(req.Item) + " from that const")
		case req.What == types.EvalConst, req.What == types.EvalGen:
			d.Alt("raise the limit: fuel " + strconv.FormatUint(2*fuel, 10))
		default:
			d.Alt("move the evaluation into a named const with fuel")
		}
		return d
	case types.EvalStage:
		var keys []string
		for _, it := range err.Items {
			if it != types.NoItem {
				keys = append(keys, items.Get(it).Key.String())
			}
		}
		last := "a same-module item"
		if n := len(err.Items); n > 0 && err.Items[n-1] != types.NoItem {
			last = name(err.Items[n-1])
		}
		return diag.New(diag.E0908, site, "this evaluation enters "+last+", which is in the same module").
			Fact("items", diag.TextList(keys...)).Fact("trace", outermost()).
			Alt("move the helper to a submodule")
	case types.EvalCycle:
		var names []string
		for _, it := range err.Items {
			names = append(names, name(it))
		}
		chain := strings.Join(append(names, names[0]), " -> ")
		return diag.New(diag.E0909, site, "const cycle: "+chain).Fact("cycle", diag.TextList(names...))
	case types.EvalDataFile:
		return DataFileDiag(site, err).Fact("trace", innermost())
	}
	source.ICEf(site, "eval.Render: error kind %d", err.Kind)
	return nil
}

// DataFileDiag is E0910 file-input at site for a failed build.File or
// Gen.Input read: facts path and reason. check's renderEval
// and gen give it the same way.
func DataFileDiag(site source.Span, err *types.EvalError) *diag.Diagnostic {
	why := map[types.DataFileError]string{
		types.DataFilePath:       "is not a relative `/` path with no `..`, `.` or empty element, drive or backslash",
		types.DataFileMissing:    "names no file under the package root",
		types.DataFileNotRegular: "names something that is not a regular file",
		types.DataFileLink:       "is reached through a link",
		types.DataFileTooLarge:   "names a file of more than 64 MiB",
	}[err.File]
	d := diag.New(diag.E0910, site, "the data file path \""+err.Path+"\" "+why).
		Text("path", err.Path).Text("reason", err.File.String())
	switch err.File {
	case types.DataFilePath:
		d.Alt("write the path relative to the package root, with `/` and no `..`")
	case types.DataFileMissing:
		d.Alt("create the file, or correct the path: it is relative to the root of the package holding the reading module")
	case types.DataFileLink:
		d.Alt("read the file where it is, not through a link")
	case types.DataFileTooLarge:
		d.Alt("split the file: a data file is at most 64 MiB")
	}
	return d
}
