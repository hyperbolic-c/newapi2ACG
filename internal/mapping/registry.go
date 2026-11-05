package mapping

import (
	"context"
	"fmt"
	"slices"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

var profiles = map[string]func() Mapper{
	"passthrough": func() Mapper { return Passthrough{} },
}

// New returns the mapper for a configured profile.
func New(profile string) (Mapper, error) {
	factory, ok := profiles[profile]
	if !ok {
		return nil, fmt.Errorf("mapping profile %q is not available (available: %v)", profile, Profiles())
	}
	return factory(), nil
}

// Profiles lists the available profile names.
func Profiles() []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Passthrough maps each source row to one target resource of kind = dataset
// and id = primary key, carrying all columns. It exists to exercise the sync
// framework end to end before business mapping rules are defined.
type Passthrough struct{}

func (Passthrough) Transform(_ context.Context, batch event.ChangeBatch) ([]target.TargetOperation, error) {
	var ops []target.TargetOperation
	for _, ev := range batch.Events {
		switch ev.Operation {
		case event.OpSnapshot, event.OpInsert:
			if ev.After == nil {
				return nil, fmt.Errorf("%s event without row data", ev.Operation)
			}
			ops = append(ops, upsert(ev.After))
		case event.OpUpdate:
			if ev.Before == nil || ev.After == nil {
				return nil, fmt.Errorf("update event without before/after rows")
			}
			if ev.Before.Key != ev.After.Key { // primary key changed
				ops = append(ops, remove(ev.Before))
			}
			ops = append(ops, upsert(ev.After))
		case event.OpDelete:
			if ev.Before == nil {
				return nil, fmt.Errorf("delete event without row data")
			}
			ops = append(ops, remove(ev.Before))
		default:
			return nil, fmt.Errorf("unknown operation %q", ev.Operation)
		}
	}
	return ops, nil
}

func identity(r *event.Record) target.TargetIdentity {
	return target.TargetIdentity{Kind: r.Dataset, ID: r.Key}
}

func upsert(r *event.Record) target.TargetOperation {
	return target.TargetOperation{Identity: identity(r), Action: target.ActionUpsert,
		Source: target.SourceRef{Dataset: r.Dataset, Key: r.Key}, Data: r.Data}
}

func remove(r *event.Record) target.TargetOperation {
	return target.TargetOperation{Identity: identity(r), Action: target.ActionDelete,
		Source: target.SourceRef{Dataset: r.Dataset, Key: r.Key}}
}
