package model

import (
	"encoding/json"
	"testing"
)

func TestIdentityRoundTrip(t *testing.T) {
	const input = "newapi-prod/Provider/channel:12"
	id, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if id.Instance != "newapi-prod" || id.Kind != ProviderKind || id.Local != "channel:12" || id.String() != input {
		t.Fatalf("unexpected identity: %#v", id)
	}
}

func TestIdentityRequiresInstance(t *testing.T) {
	for _, input := range []string{"Provider/channel:12", "/Provider/channel:12", "", "prod/Provider/"} {
		if _, err := Parse(input); err == nil {
			t.Errorf("accepted invalid identity %q", input)
		}
	}
}

func TestIdentityRejectsUnknownKind(t *testing.T) {
	if _, err := Parse("prod/Channel/channel:12"); err == nil {
		t.Fatal("accepted unknown kind")
	}
}

func TestIdentityLocalPartIsOpaque(t *testing.T) {
	const input = "prod/AggregateRoute/route:org/model:v1"
	id, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if id.Local != "route:org/model:v1" || id.String() != input {
		t.Fatalf("lost local identity: %v", id)
	}
}

func TestIdentityJSONRoundTrip(t *testing.T) {
	id, err := Parse("prod/ApiKey/token:3")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"prod/ApiKey/token:3"` {
		t.Fatalf("unexpected wire format: %s", data)
	}
	var got Identity
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Fatalf("round trip: %#v", got)
	}
	if err := json.Unmarshal([]byte(`"Provider/token:3"`), &got); err == nil {
		t.Fatal("accepted missing instance")
	}
}

func TestIdentityRejectsInvalidConstructedValues(t *testing.T) {
	for _, id := range []Identity{
		{}, {Instance: "prod/other", Kind: ProviderKind, Local: "channel:1"},
		{Instance: " ", Kind: ProviderKind, Local: "channel:1"},
		{Instance: "prod", Kind: ProviderKind, Local: "\x00"},
	} {
		if _, err := json.Marshal(id); err == nil {
			t.Errorf("serialized invalid identity %#v", id)
		}
	}
}
