package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testID(kind Kind, local string) Identity {
	return Identity{Instance: "source", Kind: kind, Local: local}
}
func testResources() []Resource {
	meta := func(k Kind, l string) Metadata { return Metadata{SourceIdentity: testID(k, l), MigrationJob: "job"} }
	return []Resource{
		{APIVersion: "acg/v1", Kind: GroupKind, Metadata: meta(GroupKind, "group:a"), Group: &Group{Name: "a"}},
		{APIVersion: "acg/v1", Kind: ProviderKind, Metadata: meta(ProviderKind, "channel:1"), Provider: &Provider{Name: "p", Type: "openai", Endpoint: "https://example.test/v1", Enabled: true, APIKeys: []SecretRef{{Fingerprint: strings.Repeat("a", 64)}}}},
		{APIVersion: "acg/v1", Kind: AggregateRouteKind, Metadata: meta(AggregateRouteKind, "route:m"), AggregateRoute: &AggregateRoute{Name: "m", Members: []RouteMember{{ProviderRef: testID(ProviderKind, "channel:1"), Priority: 1, Weight: 10}}}},
		{APIVersion: "acg/v1", Kind: UserKind, Metadata: meta(UserKind, "user:1"), User: &User{Username: "alice", Role: "user", Enabled: true, GroupRef: testID(GroupKind, "group:a")}},
		{APIVersion: "acg/v1", Kind: ApiKeyKind, Metadata: meta(ApiKeyKind, "token:1"), ApiKey: &ApiKey{Name: "key", UserRef: testID(UserKind, "user:1"), Key: SecretRef{Fingerprint: strings.Repeat("b", 64)}, Enabled: true, ProviderRefs: []Identity{testID(ProviderKind, "channel:1")}, RouteRefs: []Identity{testID(AggregateRouteKind, "route:m")}}},
	}
}

func TestResourceValidate(t *testing.T) {
	for _, r := range testResources() {
		if err := r.Validate(); err != nil {
			t.Fatalf("%s: %v", r.Kind, err)
		}
	}
	cases := []struct {
		name   string
		change func(*Resource)
		index  int
	}{
		{"name", func(r *Resource) { r.Provider.Name = "" }, 1},
		{"metadata", func(r *Resource) { r.Metadata.MigrationJob = "" }, 0},
		{"wrong kind", func(r *Resource) { r.Kind = UserKind }, 0},
		{"extra payload", func(r *Resource) { r.User = &User{} }, 0},
		{"wrong ref kind", func(r *Resource) { r.ApiKey.UserRef = testID(ProviderKind, "channel:1") }, 4},
		{"cross source", func(r *Resource) { r.ApiKey.UserRef.Instance = "other" }, 4},
		{"empty route", func(r *Resource) { r.AggregateRoute.Members = nil }, 2},
		{"negative weight", func(r *Resource) { r.AggregateRoute.Members[0].Weight = -1 }, 2},
		{"raw secret", func(r *Resource) { r.ApiKey.Key.Fingerprint = "raw-key-must-not-appear" }, 4},
		{"invalid cidr", func(r *Resource) { r.ApiKey.IPAllowlist = []string{"not-an-ip"} }, 4},
		{"endpoint credentials", func(r *Resource) { r.Provider.Endpoint = "https://user:pass@example.test" }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testResources()[tc.index]
			tc.change(&r)
			err := r.Validate()
			if err == nil {
				t.Fatal("invalid resource accepted")
			}
			if strings.Contains(err.Error(), "raw-key-must-not-appear") || strings.Contains(err.Error(), "pass@") {
				t.Fatal("validation leaked input")
			}
		})
	}
}

func TestResourceDecodeRejectsUnknownFields(t *testing.T) {
	r := testResources()[0]
	b, _ := json.Marshal(r)
	var object map[string]any
	_ = json.Unmarshal(b, &object)
	object["typo"] = "value"
	b, _ = json.Marshal(object)
	if _, err := Decode(b); err == nil {
		t.Fatal("accepted unknown field")
	}
	b, _ = json.Marshal(r)
	if _, err := Decode(append(b, []byte(` {}`)...)); err == nil {
		t.Fatal("accepted extra document")
	}
	decoded, err := Decode(b)
	if err != nil || decoded.Group.Name != "a" {
		t.Fatalf("decode: %v", err)
	}
}

func TestHashIgnoresSetOrder(t *testing.T) {
	for _, index := range []int{1, 2, 4} {
		t.Run(string(testResources()[index].Kind), func(t *testing.T) {
			a := testResources()[index]
			b := testResources()[index]
			switch index {
			case 1:
				a.Provider.APIKeys = append(a.Provider.APIKeys, SecretRef{Fingerprint: strings.Repeat("c", 64)})
				b.Provider.APIKeys = []SecretRef{a.Provider.APIKeys[1], a.Provider.APIKeys[0]}
			case 2:
				a.AggregateRoute.Members = append(a.AggregateRoute.Members, RouteMember{ProviderRef: testID(ProviderKind, "channel:2"), Weight: 20})
				b.AggregateRoute.Members = []RouteMember{a.AggregateRoute.Members[1], a.AggregateRoute.Members[0]}
			case 4:
				a.ApiKey.ProviderRefs = append(a.ApiKey.ProviderRefs, testID(ProviderKind, "channel:2"))
				b.ApiKey.ProviderRefs = []Identity{a.ApiKey.ProviderRefs[1], a.ApiKey.ProviderRefs[0]}
				a.ApiKey.RouteRefs = append(a.ApiKey.RouteRefs, testID(AggregateRouteKind, "route:n"))
				b.ApiKey.RouteRefs = []Identity{a.ApiKey.RouteRefs[1], a.ApiKey.RouteRefs[0]}
			}
			x, err := Hash(a)
			if err != nil {
				t.Fatal(err)
			}
			y, err := Hash(b)
			if err != nil {
				t.Fatal(err)
			}
			if x != y {
				t.Fatal("set order affected hash")
			}
		})
	}
}

func TestHashDedupsSets(t *testing.T) {
	for _, index := range []int{1, 2, 4} {
		a := testResources()[index]
		b := testResources()[index]
		switch index {
		case 1:
			b.Provider.APIKeys = append(b.Provider.APIKeys, b.Provider.APIKeys[0])
		case 2:
			b.AggregateRoute.Members = append(b.AggregateRoute.Members, b.AggregateRoute.Members[0])
		case 4:
			b.ApiKey.ProviderRefs = append(b.ApiKey.ProviderRefs, b.ApiKey.ProviderRefs[0])
			b.ApiKey.RouteRefs = append(b.ApiKey.RouteRefs, b.ApiKey.RouteRefs[0])
		}
		x, _ := Hash(a)
		y, err := Hash(b)
		if err != nil || x != y {
			t.Fatalf("dedup %s: %v", a.Kind, err)
		}
	}
}

func TestHashExcludesInitOnlyFields(t *testing.T) {
	a := testResources()[3]
	b := testResources()[3]
	b.User.Initialization = &UserInitialization{PasswordEnv: "PASSWORD_ENV", MustChangePassword: true, ExpiresInSeconds: 604800}
	b.Metadata.ContentHash = strings.Repeat("c", 64)
	x, _ := Hash(a)
	y, err := Hash(b)
	if err != nil || x != y {
		t.Fatal("initialization or previous content hash affects managed hash")
	}
	data, _ := json.Marshal(b)
	if strings.Contains(string(data), "PASSWORD_ENV") {
		t.Fatal("initialization persisted in resource")
	}
}

func TestHashChangesOnManagedField(t *testing.T) {
	for i := range testResources() {
		a := testResources()[i]
		b := testResources()[i]
		switch i {
		case 0:
			b.Group.Name = "other"
		case 1:
			b.Provider.Enabled = false
		case 2:
			b.AggregateRoute.Members[0].Priority = 9
		case 3:
			b.User.Role = "admin"
		case 4:
			b.ApiKey.Enabled = false
		}
		x, _ := Hash(a)
		y, err := Hash(b)
		if err != nil || x == y {
			t.Fatalf("managed change ignored for %s", a.Kind)
		}
	}
}

func TestCanonicalizeDoesNotMutateInput(t *testing.T) {
	r := testResources()[4]
	r.ApiKey.ProviderRefs = []Identity{testID(ProviderKind, "channel:2"), testID(ProviderKind, "channel:1")}
	before, _ := json.Marshal(r)
	if _, err := Canonicalize(r); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("canonicalization mutated caller data")
	}
}

func TestConflictingDuplicateMembersRejected(t *testing.T) {
	r := testResources()[2]
	r.AggregateRoute.Members = append(r.AggregateRoute.Members, RouteMember{ProviderRef: r.AggregateRoute.Members[0].ProviderRef, Weight: 999})
	if _, err := Hash(r); err == nil {
		t.Fatal("conflicting member accepted")
	}
}

func TestHashNormalizesTimeAndEmptyCollections(t *testing.T) {
	a := testResources()[4]
	b := testResources()[4]
	now := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	other := now.In(time.FixedZone("plus8", 8*3600))
	a.ApiKey.ExpiresAt = &now
	b.ApiKey.ExpiresAt = &other
	b.ApiKey.IPAllowlist = []string{}
	x, _ := Hash(a)
	y, err := Hash(b)
	if err != nil || x != y {
		t.Fatal("equivalent time/empty collection changed hash")
	}
}

func TestDecodeRequiresExplicitManagedBooleans(t *testing.T) {
	for _, index := range []int{1, 3, 4} {
		r := testResources()[index]
		data, _ := json.Marshal(r)
		var object map[string]any
		_ = json.Unmarshal(data, &object)
		field := map[Kind]string{ProviderKind: "provider", UserKind: "user", ApiKeyKind: "apikey"}[r.Kind]
		for _, mode := range []string{"missing", "null"} {
			payload := object[field].(map[string]any)
			if mode == "missing" {
				delete(payload, "enabled")
			} else {
				payload["enabled"] = nil
			}
			malformed, _ := json.Marshal(object)
			if _, err := Decode(malformed); err == nil {
				t.Errorf("%s: accepted %s enabled", r.Kind, mode)
			}
		}
	}
}

func TestDecodeRejectsMissingRouteMemberNumbers(t *testing.T) {
	for _, field := range []string{"priority", "weight"} {
		for _, null := range []bool{false, true} {
			r := testResources()[2]
			data, _ := json.Marshal(r)
			var object map[string]any
			_ = json.Unmarshal(data, &object)
			member := object["aggregate_route"].(map[string]any)["members"].([]any)[0].(map[string]any)
			if null {
				member[field] = nil
			} else {
				delete(member, field)
			}
			data, _ = json.Marshal(object)
			if _, err := Decode(data); err == nil {
				t.Errorf("accepted missing/null %s", field)
			}
		}
	}
}
func TestDecodeRejectsExtraNullPayload(t *testing.T) {
	r := testResources()[0]
	data, _ := json.Marshal(r)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	object["user"] = nil
	data, _ = json.Marshal(object)
	if _, err := Decode(data); err == nil {
		t.Fatal("accepted a second null payload")
	}
}
