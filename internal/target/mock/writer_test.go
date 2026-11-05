package mock

import (
	"context"
	"errors"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

var id = target.TargetIdentity{Kind: "users", ID: "1"}

func upsert(v string) []target.TargetOperation {
	return []target.TargetOperation{{Identity: id, Action: target.ActionUpsert, Data: map[string]any{"name": v}}}
}

func TestUpsertIsIdempotent(t *testing.T) {
	w := New()
	ctx := context.Background()
	r1, err := w.Apply(ctx, upsert("a"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := w.Apply(ctx, upsert("b"))
	if err != nil {
		t.Fatal(err)
	}
	if r1[0].TargetID != r2[0].TargetID || len(w.Resources()) != 1 || w.Resources()[id]["name"] != "b" {
		t.Fatalf("not idempotent: %v %v %v", r1, r2, w.Resources())
	}
	if _, err := w.Apply(ctx, []target.TargetOperation{{Identity: id, Action: target.ActionDelete}}); err != nil || len(w.Resources()) != 0 {
		t.Fatal("delete failed")
	}
}

func TestFaults(t *testing.T) {
	w := New()
	ctx := context.Background()
	w.InjectFaults(FaultFail, FaultLostResponse, FaultPermanent)
	if _, err := w.Apply(ctx, upsert("a")); !errors.Is(err, ErrTransient) || len(w.Resources()) != 0 {
		t.Fatal("FaultFail applied changes")
	}
	if _, err := w.Apply(ctx, upsert("a")); !errors.Is(err, ErrTransient) || len(w.Resources()) != 1 {
		t.Fatal("FaultLostResponse did not apply")
	}
	if _, err := w.Apply(ctx, upsert("b")); !target.IsPermanent(err) || w.Resources()[id]["name"] != "a" {
		t.Fatal("FaultPermanent applied changes")
	}
	got, err := w.Inspect(ctx, []target.TargetIdentity{id, {Kind: "users", ID: "absent"}})
	if err != nil || len(got) != 1 || got[0].Data["name"] != "a" {
		t.Fatalf("inspect %v %v", got, err)
	}
}
