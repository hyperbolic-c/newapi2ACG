package memory_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
	"github.com/hyperbolic-c/newapi2ACG/internal/target/memory"
	"github.com/hyperbolic-c/newapi2ACG/test/contract"
)

func TestTargetContract(t *testing.T) { contract.Run(t, func() target.Target { return memory.New() }) }
func provider(local, name string, enabled bool) model.Resource {
	return model.Resource{APIVersion: "acg/v1", Kind: model.ProviderKind, Metadata: model.Metadata{SourceIdentity: model.Identity{Instance: "source", Kind: model.ProviderKind, Local: local}, MigrationJob: "job"}, Provider: &model.Provider{Name: name, Type: "openai", Endpoint: "https://example.test", Enabled: enabled, APIKeys: []model.SecretRef{{Fingerprint: strings.Repeat("a", 64)}}}}
}
func setupRouting(t *testing.T) (*memory.Target, model.Identity, model.Identity) {
	t.Helper()
	adapter := memory.New()
	p1 := provider("channel:1", "p1", true)
	p2 := provider("channel:2", "p2", true)
	p3 := provider("channel:3", "p3", false)
	group := contract.Group("group:a", "a")
	userID := model.Identity{Instance: "source", Kind: model.UserKind, Local: "user:1"}
	routeID := model.Identity{Instance: "source", Kind: model.AggregateRouteKind, Local: "route:m"}
	keyID := model.Identity{Instance: "source", Kind: model.ApiKeyKind, Local: "token:1"}
	user := model.Resource{APIVersion: "acg/v1", Kind: model.UserKind, Metadata: model.Metadata{SourceIdentity: userID, MigrationJob: "job"}, User: &model.User{Username: "alice", Role: "user", Enabled: true, GroupRef: group.Metadata.SourceIdentity}}
	route := model.Resource{APIVersion: "acg/v1", Kind: model.AggregateRouteKind, Metadata: model.Metadata{SourceIdentity: routeID, MigrationJob: "job"}, AggregateRoute: &model.AggregateRoute{Name: "m", Members: []model.RouteMember{{ProviderRef: p1.Metadata.SourceIdentity, Priority: 1, Weight: 10}, {ProviderRef: p2.Metadata.SourceIdentity, Priority: 99, Weight: 10}, {ProviderRef: p3.Metadata.SourceIdentity, Priority: 100, Weight: 10}}}}
	key := model.Resource{APIVersion: "acg/v1", Kind: model.ApiKeyKind, Metadata: model.Metadata{SourceIdentity: keyID, MigrationJob: "job"}, ApiKey: &model.ApiKey{Name: "k", Enabled: true, UserRef: userID, Key: model.SecretRef{Fingerprint: strings.Repeat("b", 64)}, ProviderRefs: []model.Identity{p1.Metadata.SourceIdentity, p3.Metadata.SourceIdentity}, RouteRefs: []model.Identity{routeID}}}
	for i, r := range []model.Resource{group, p1, p2, p3, user, route, key} {
		results, err := adapter.Apply(context.Background(), []model.Change{contract.Change(string(rune('a'+i)), r)})
		if err != nil || results[0].Outcome != model.Succeeded {
			t.Fatalf("setup: %+v %v", results, err)
		}
	}
	return adapter, keyID, p1.Metadata.SourceIdentity
}
func TestRoutingFiltersByGrantBeforeSelection(t *testing.T) {
	adapter, key, want := setupRouting(t)
	got, err := adapter.Candidates(context.Background(), key, "m")
	if err != nil || len(got) != 1 || got[0].ProviderRef != want {
		t.Fatalf("candidates: %+v %v", got, err)
	}
	picked, err := adapter.Select(context.Background(), key, "m", 0)
	if err != nil || picked != want {
		t.Fatalf("selection: %v %v", picked, err)
	}
	if _, err := adapter.Candidates(context.Background(), key, "p2/m"); err == nil {
		t.Fatal("unauthorized direct route accepted")
	}
}
func TestFaultInjection(t *testing.T) {
	for _, fault := range []memory.Fault{memory.FailBefore, memory.TimeoutBefore, memory.LoseResponseAfter} {
		adapter := memory.New()
		adapter.Inject("op", fault)
		change := contract.Change("op", contract.Group("group:a", "a"))
		results, err := adapter.Apply(context.Background(), []model.Change{change})
		if err != nil {
			t.Fatal(err)
		}
		want := model.Failed
		if fault == memory.TimeoutBefore {
			want = model.Retryable
		}
		if fault == memory.LoseResponseAfter {
			want = model.Unknown
		}
		if results[0].Outcome != want {
			t.Fatalf("fault outcome: %+v", results)
		}
		observed, _ := adapter.Observe(context.Background(), nil)
		expectedCount := 0
		if fault == memory.LoseResponseAfter {
			expectedCount = 1
		}
		if len(observed.Items) != expectedCount {
			t.Fatal("fault mutated wrong state")
		}
	}
}
func TestObserveMarksWriteOnlyFields(t *testing.T) {
	adapter := memory.New()
	_, _ = adapter.Apply(context.Background(), []model.Change{contract.Change("op", provider("channel:1", "p1", true))})
	obs, err := adapter.Observe(context.Background(), nil)
	if err != nil || len(obs.Items) != 1 {
		t.Fatal(err)
	}
	if len(obs.Items[0].Unreadable) != 1 || obs.Items[0].Unreadable[0] != "provider.api_keys" || len(obs.Items[0].Resource.Provider.APIKeys) != 0 {
		t.Fatal("write-only field appeared readable")
	}
}
func TestReusedIdempotencyKeyWithDifferentContentFails(t *testing.T) {
	adapter := memory.New()
	a := contract.Change("a", contract.Group("group:a", "a"))
	_, _ = adapter.Apply(context.Background(), []model.Change{a})
	b := contract.Change("b", contract.Group("group:b", "b"))
	b.IdemKey = a.IdemKey
	results, _ := adapter.Apply(context.Background(), []model.Change{b})
	if results[0].Outcome != model.Failed {
		t.Fatal("idempotency collision silently accepted")
	}
}

func TestApiKeyNamesNeedNotBeUnique(t *testing.T) {
	adapter, _, _ := setupRouting(t)
	observed, _ := adapter.Observe(context.Background(), []model.Kind{model.ApiKeyKind})
	key := observed.Items[0].Resource
	key.Metadata.SourceIdentity.Local = "token:2"
	key.ApiKey.Key.Fingerprint = strings.Repeat("c", 64)
	result, _ := adapter.Apply(context.Background(), []model.Change{contract.Change("same-name", key)})
	if result[0].Outcome != model.Succeeded {
		t.Fatalf("duplicate key display name rejected: %+v", result)
	}
	key.Metadata.SourceIdentity.Local = "token:3"
	key.ApiKey.Name = "other-name"
	result, _ = adapter.Apply(context.Background(), []model.Change{contract.Change("same-key", key)})
	if result[0].Outcome != model.Failed {
		t.Fatal("duplicate fingerprint accepted")
	}
}
func TestAuthorizationOperationsAndDeleteDependencies(t *testing.T) {
	adapter, keyID, providerID := setupRouting(t)
	obs, _ := adapter.Observe(context.Background(), []model.Kind{model.ApiKeyKind})
	key := obs.Items[0].Resource
	key.ApiKey.Key.Fingerprint = strings.Repeat("b", 64)
	key.ApiKey.RouteRefs = nil
	change := contract.Change("revoke", key)
	change.Op = model.Revoke
	change.IdemKey = "revoke-1"
	results, _ := adapter.Apply(context.Background(), []model.Change{change})
	if results[0].Outcome != model.Succeeded {
		t.Fatalf("revoke: %+v", results)
	}
	if _, err := adapter.Candidates(context.Background(), keyID, "m"); err == nil {
		t.Fatal("revoked route still available")
	}
	key.ApiKey.RouteRefs = []model.Identity{{Instance: "source", Kind: model.AggregateRouteKind, Local: "route:m"}}
	change = contract.Change("grant", key)
	change.Op = model.Grant
	change.IdemKey = "grant-1"
	results, _ = adapter.Apply(context.Background(), []model.Change{change})
	if results[0].Outcome != model.Succeeded {
		t.Fatalf("grant: %+v", results)
	}
	key.ApiKey.Enabled = false
	change = contract.Change("disable", key)
	change.Op = model.Disable
	change.IdemKey = "disable-1"
	results, _ = adapter.Apply(context.Background(), []model.Change{change})
	if results[0].Outcome != model.Succeeded {
		t.Fatalf("disable: %+v", results)
	}
	if _, err := adapter.Candidates(context.Background(), keyID, "m"); err == nil {
		t.Fatal("disabled key still available")
	}
	p := provider(providerID.Local, "p1", true)
	change = contract.Change("delete-provider", p)
	change.Op = model.Delete
	change.IdemKey = "delete-provider"
	results, _ = adapter.Apply(context.Background(), []model.Change{change})
	if results[0].Outcome != model.Failed {
		t.Fatal("deleted referenced provider")
	}
	change = contract.Change("delete-key", key)
	change.Op = model.Delete
	change.IdemKey = "delete-key"
	results, _ = adapter.Apply(context.Background(), []model.Change{change})
	if results[0].Outcome != model.Succeeded {
		t.Fatalf("delete key: %+v", results)
	}
}
func TestWeightedSelectionBoundaries(t *testing.T) {
	adapter, keyID, p1 := setupRouting(t)
	obs, _ := adapter.Observe(context.Background(), []model.Kind{model.AggregateRouteKind})
	route := obs.Items[0].Resource
	p2 := model.Identity{Instance: "source", Kind: model.ProviderKind, Local: "channel:2"}
	route.AggregateRoute.Members = []model.RouteMember{{ProviderRef: p1, Priority: 1, Weight: 1}, {ProviderRef: p2, Priority: 1, Weight: 3}}
	change := contract.Change("route-update", route)
	change.Op = model.Update
	change.IdemKey = "route-update"
	results, _ := adapter.Apply(context.Background(), []model.Change{change})
	if results[0].Outcome != model.Succeeded {
		t.Fatalf("route update: %+v", results)
	}
	obs, _ = adapter.Observe(context.Background(), []model.Kind{model.ApiKeyKind})
	key := obs.Items[0].Resource
	key.ApiKey.Key.Fingerprint = strings.Repeat("b", 64)
	key.ApiKey.ProviderRefs = append(key.ApiKey.ProviderRefs, p2)
	change = contract.Change("key-update", key)
	change.Op = model.Update
	change.IdemKey = "key-update"
	results, _ = adapter.Apply(context.Background(), []model.Change{change})
	if results[0].Outcome != model.Succeeded {
		t.Fatalf("key update: %+v", results)
	}
	for ticket, want := range []model.Identity{p1, p2, p2, p2, p1} {
		got, err := adapter.Select(context.Background(), keyID, "m", uint64(ticket))
		if err != nil || got != want {
			t.Fatalf("ticket %d: %v %v", ticket, got, err)
		}
	}
}
func TestCloneDoesNotShareState(t *testing.T) {
	adapter := memory.New()
	_, _ = adapter.Apply(context.Background(), []model.Change{contract.Change("a", contract.Group("group:a", "a"))})
	clone, err := adapter.Clone()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = clone.Apply(context.Background(), []model.Change{contract.Change("b", contract.Group("group:b", "b"))})
	original, _ := adapter.Observe(context.Background(), nil)
	copied, _ := clone.Observe(context.Background(), nil)
	if len(original.Items) != 1 || len(copied.Items) != 2 {
		t.Fatal("clone shared mutable state")
	}
	snapshot, err := clone.Snapshot(context.Background())
	if err != nil || len(snapshot) != 2 {
		t.Fatalf("snapshot: %v", err)
	}
	snapshot[0].Group.Name = "mutated"
	again, _ := clone.Snapshot(context.Background())
	if again[0].Group.Name == "mutated" {
		t.Fatal("snapshot shared mutable state")
	}
}

func TestDuplicateOperationIDsRejected(t *testing.T) {
	contract.RunDuplicateOperationCheck(t, memory.New())
}

func TestResourceCanReturnToPreviouslyAppliedContent(t *testing.T) {
	adapter := memory.New()
	a := contract.Group("group:a", "a")
	_, _ = adapter.Apply(context.Background(), []model.Change{contract.Change("create", a)})
	apply := func(opID, name string) {
		t.Helper()
		r := contract.Group("group:a", name)
		change := contract.Change(opID, r)
		change.Op = model.Update
		results, err := adapter.Apply(context.Background(), []model.Change{change})
		if err != nil || results[0].Outcome != model.Succeeded {
			t.Fatalf("%s: %+v %v", opID, results, err)
		}
	}
	apply("to-b", "b")
	apply("to-a", "a")
	apply("to-b-again", "b")
	obs, _ := adapter.Observe(context.Background(), nil)
	if obs.Items[0].Resource.Group.Name != "b" {
		t.Fatal("new intent incorrectly reused historical content receipt")
	}
}
func TestResourceCanBeRecreatedAfterDelete(t *testing.T) {
	adapter := memory.New()
	r := contract.Group("group:a", "a")
	create := contract.Change("create", r)
	_, _ = adapter.Apply(context.Background(), []model.Change{create})
	deletion := contract.Change("delete", r)
	deletion.Op = model.Delete
	results, err := adapter.Apply(context.Background(), []model.Change{deletion})
	if err != nil || results[0].Outcome != model.Succeeded {
		t.Fatalf("delete: %+v %v", results, err)
	}
	create.OpID = "recreate"
	results, err = adapter.Apply(context.Background(), []model.Change{create})
	if err != nil || results[0].Outcome != model.Succeeded {
		t.Fatalf("recreate: %+v %v", results, err)
	}
	obs, _ := adapter.Observe(context.Background(), nil)
	if len(obs.Items) != 1 {
		t.Fatal("recreation used old receipt without recreating")
	}
}
func TestSameOperationRetryDoesNotUndoNewerWrite(t *testing.T) {
	adapter := memory.New()
	a := contract.Group("group:a", "a")
	_, _ = adapter.Apply(context.Background(), []model.Change{contract.Change("create", a)})
	old := contract.Change("old-update", contract.Group("group:a", "b"))
	old.Op = model.Update
	_, _ = adapter.Apply(context.Background(), []model.Change{old})
	latest := contract.Change("new-update", contract.Group("group:a", "c"))
	latest.Op = model.Update
	_, _ = adapter.Apply(context.Background(), []model.Change{latest})
	_, _ = adapter.Apply(context.Background(), []model.Change{old})
	obs, _ := adapter.Observe(context.Background(), nil)
	if obs.Items[0].Resource.Group.Name != "c" {
		t.Fatal("retry overwrote newer intent")
	}
}
func TestOperationIDBoundAcrossBatches(t *testing.T) {
	contract.RunOperationBindingCheck(t, memory.New())
}
