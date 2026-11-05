package mapping

import (
	"context"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

func rec(key, name string) *event.Record {
	return &event.Record{Dataset: "users", Key: key, Data: map[string]any{"id": key, "name": name}}
}

func TestPassthrough(t *testing.T) {
	m, err := New("passthrough")
	if err != nil {
		t.Fatal(err)
	}
	ops, err := m.Transform(context.Background(), event.ChangeBatch{Events: []event.ChangeEvent{
		{Operation: event.OpSnapshot, After: rec("1", "a")},
		{Operation: event.OpUpdate, Before: rec("2", "b"), After: rec("3", "b")},
		{Operation: event.OpDelete, Before: rec("4", "c")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		action target.Action
		id     string
	}{{target.ActionUpsert, "1"}, {target.ActionDelete, "2"}, {target.ActionUpsert, "3"}, {target.ActionDelete, "4"}}
	if len(ops) != len(want) {
		t.Fatalf("ops %+v", ops)
	}
	for i, w := range want {
		if ops[i].Action != w.action || ops[i].Identity != (target.TargetIdentity{Kind: "users", ID: w.id}) || ops[i].Source.Key != w.id {
			t.Errorf("op %d: %+v", i, ops[i])
		}
	}
}

func TestPassthroughRejectsMalformedEvents(t *testing.T) {
	for _, ev := range []event.ChangeEvent{
		{Operation: event.OpInsert},
		{Operation: event.OpUpdate, After: rec("1", "a")},
		{Operation: event.OpDelete},
		{Operation: "truncate"},
	} {
		if _, err := (Passthrough{}).Transform(context.Background(), event.ChangeBatch{Events: []event.ChangeEvent{ev}}); err == nil {
			t.Errorf("accepted %+v", ev)
		}
	}
}

func TestUnknownProfile(t *testing.T) {
	if _, err := New("newapi-to-acg"); err == nil {
		t.Fatal("unimplemented profile accepted")
	}
}
