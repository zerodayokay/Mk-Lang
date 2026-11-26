// Why: a cached E0910 or E0902 evaluation must hit and replay as it was
// reported, so the record of a failed data file read has to keep its reason
// and path, and that of an exhausted evaluation the fuel that ran out (a
// large const's value raises it above the stated one).
// What: TestEvalRecordDataFile, TestEvalRecordFuel.
// How: encodes and decodes an EvalRecord for a missing data file and for
// an exhausted fuel, and compares the error and the hash of its reads.

package iface

import (
	"reflect"
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/types"
)

func TestEvalRecordDataFile(t *testing.T) {
	a, f := ifaceSession(false)
	site := strings.Index(bodyText, "a[i]")
	x := &types.EvalError{Kind: types.EvalDataFile, File: types.DataFileMissing, Path: "data/in.txt", Steps: 2,
		At:    source.NewSpan(f.ID, uint32(site), uint32(site+4)),
		Trace: []source.Span{source.NewSpan(f.ID, uint32(site), uint32(site+4))},
		Items: []types.ItemID{a.id("geo.Main")}}
	rec := &EvalRecord{Err: x, Steps: 2, Site: "geo.Main"}
	rec.Reads.Add(types.Read{Kind: types.ReadData, Key: types.ModuleKey("geo"), Name: "data/in.txt"})
	got, err := DecodeEvalRecord(a.env, EncodeEvalRecord(a.env, a.h, rec))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Err, x) || got.Reads.Hash() != rec.Reads.Hash() {
		t.Errorf("eval record differs: %+v", got.Err)
	}
}

func TestEvalRecordFuel(t *testing.T) {
	a, f := ifaceSession(false)
	site := strings.Index(bodyText, "a[i]")
	x := &types.EvalError{Kind: types.EvalFuel, Steps: 8_388_600, Fuel: 8_388_608,
		At:    source.NewSpan(f.ID, uint32(site), uint32(site+4)),
		Trace: []source.Span{source.NewSpan(f.ID, uint32(site), uint32(site+4))},
		Items: []types.ItemID{a.id("geo.Main")}}
	rec := &EvalRecord{Err: x, Steps: 8_388_600, Site: "geo.Main"}
	got, err := DecodeEvalRecord(a.env, EncodeEvalRecord(a.env, a.h, rec))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Err, x) {
		t.Errorf("eval record differs: %+v", got.Err)
	}
}
