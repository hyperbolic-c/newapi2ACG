// Package model defines the target-independent acg/v1 resource contracts.
package model

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Kind is one of the five resource kinds supported by acg/v1.
type Kind string

const (
	GroupKind          Kind = "Group"
	ProviderKind       Kind = "Provider"
	AggregateRouteKind Kind = "AggregateRoute"
	UserKind           Kind = "User"
	ApiKeyKind         Kind = "ApiKey"
)

func (k Kind) Valid() bool {
	switch k {
	case GroupKind, ProviderKind, AggregateRouteKind, UserKind, ApiKeyKind:
		return true
	default:
		return false
	}
}

// Identity is a stable source identity. Local is opaque, including any slashes.
type Identity struct {
	Instance string
	Kind     Kind
	Local    string
}

func Parse(raw string) (Identity, error) {
	parts := strings.SplitN(raw, "/", 3)
	if len(parts) != 3 {
		return Identity{}, errors.New("identity requires instance/kind/local")
	}
	id := Identity{Instance: parts[0], Kind: Kind(parts[1]), Local: parts[2]}
	if err := id.Validate(); err != nil {
		return Identity{}, err
	}
	return id, nil
}

func (id Identity) Validate() error {
	if strings.TrimSpace(id.Instance) == "" || strings.Contains(id.Instance, "/") || strings.IndexFunc(id.Instance, unicode.IsSpace) >= 0 {
		return errors.New("identity instance must be nonempty and contain no slash or whitespace")
	}
	if !id.Kind.Valid() {
		return errors.New("identity has unsupported resource kind")
	}
	if strings.TrimSpace(id.Local) == "" {
		return errors.New("identity local part is required")
	}
	if strings.IndexFunc(id.Instance+id.Local, unicode.IsControl) >= 0 {
		return errors.New("identity contains control characters")
	}
	return nil
}

func (id Identity) String() string {
	return id.Instance + "/" + string(id.Kind) + "/" + id.Local
}

// MarshalText also makes Identity usable as a JSON object key.
func (id Identity) MarshalText() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("marshal identity: %w", err)
	}
	return []byte(id.String()), nil
}

func (id *Identity) UnmarshalText(data []byte) error {
	parsed, err := Parse(string(data))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
