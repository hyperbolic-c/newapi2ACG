// Package contract contains reusable target-adapter behavioral tests.
package contract

import (
	"context"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

func Group(local, name string) model.Resource {
	return model.Resource{APIVersion: "acg/v1", Kind: model.GroupKind, Metadata: model.Metadata{SourceIdentity: model.Identity{Instance: "source", Kind: model.GroupKind, Local: local}, MigrationJob: "job"}, Group: &model.Group{Name: name}}
}
func Change(id string, r model.Resource) model.Change {
	hash, _ := model.Hash(r)
	return model.Change{OpID: id, Op: model.Create, Resource: r, IdemKey: r.Metadata.SourceIdentity.String() + ":" + hash}
}
func Run(t *testing.T, newTarget func() target.Target) {
	t.Helper()
	t.Run("idempotent_replay", func(t *testing.T) {
		adapter := newTarget()
		change := Change("first", Group("group:a", "a"))
		first, err := adapter.Apply(context.Background(), []model.Change{change})
		if err != nil || len(first) != 1 || first[0].Outcome != model.Succeeded {
			t.Fatalf("first apply: %+v %v", first, err)
		}
		change.OpID = "replay"
		second, err := adapter.Apply(context.Background(), []model.Change{change})
		if err != nil || len(second) != 1 || second[0].Outcome != model.Succeeded || second[0].TargetID != first[0].TargetID || second[0].OpID != "replay" {
			t.Fatalf("replay: %+v %v", second, err)
		}
		observed, err := adapter.Observe(context.Background(), []model.Kind{model.GroupKind})
		if err != nil || len(observed.Items) != 1 {
			t.Fatalf("observe: %+v %v", observed, err)
		}
	})
	t.Run("conflict_preserves_successes", func(t *testing.T) {
		adapter := newTarget()
		changes := []model.Change{Change("a", Group("group:a", "a")), Change("conflict", Group("group:b", "a")), Change("c", Group("group:c", "c"))}
		results, err := adapter.Apply(context.Background(), changes)
		if err != nil {
			t.Fatal(err)
		}
		outcomes := map[string]model.Outcome{}
		for _, r := range results {
			outcomes[r.OpID] = r.Outcome
		}
		if outcomes["a"] != model.Succeeded || outcomes["conflict"] != model.Failed || outcomes["c"] != model.Succeeded {
			t.Fatalf("partial batch: %+v", results)
		}
		obs, err := adapter.Observe(context.Background(), nil)
		if err != nil || len(obs.Items) != 2 {
			t.Fatalf("successful writes lost: %+v %v", obs, err)
		}
	})
}

// RunDuplicateOperationCheck verifies that OpID correlation is unambiguous and
// malformed batches cannot partially mutate the target.
func RunDuplicateOperationCheck(t *testing.T, adapter target.Target) {
	t.Helper()
	a := Change("duplicate", Group("group:a", "a"))
	b := Change("duplicate", Group("group:b", "b"))
	if _, err := adapter.Apply(context.Background(), []model.Change{a, b}); err == nil {
		t.Fatal("duplicate operation IDs accepted")
	}
	observed, err := adapter.Observe(context.Background(), nil)
	if err != nil || len(observed.Items) != 0 {
		t.Fatalf("invalid batch mutated state: %+v %v", observed, err)
	}
}

func RunOperationBindingCheck(t *testing.T, adapter target.Target) {
	t.Helper()
	first := Change("bound", Group("group:a", "a"))
	results, err := adapter.Apply(context.Background(), []model.Change{first})
	if err != nil || results[0].Outcome != model.Succeeded {
		t.Fatalf("first operation failed: %+v %v", results, err)
	}
	changed := Change("bound", Group("group:b", "b"))
	results, err = adapter.Apply(context.Background(), []model.Change{changed})
	if err != nil || results[0].Outcome != model.Failed {
		t.Fatalf("operation ID binding lost: %+v %v", results, err)
	}
	observed, _ := adapter.Observe(context.Background(), nil)
	if len(observed.Items) != 1 || observed.Items[0].Resource.Group.Name != "a" {
		t.Fatal("conflicting intent mutated state")
	}
}
