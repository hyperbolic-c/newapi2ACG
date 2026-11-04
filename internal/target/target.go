// Package target defines adapters at the logical resource boundary.
package target

import (
	"context"
	"github.com/hyperbolic-c/newapi2ACG/internal/model"
)

type Capabilities struct {
	SourceIdentity  bool
	Idempotency     bool
	AtomicKeyGrants bool
	OriginalKeys    bool
}
type Target interface {
	Capabilities(context.Context) (Capabilities, error)
	Observe(context.Context, []model.Kind) (*model.Observed, error)
	Apply(context.Context, []model.Change) ([]model.Result, error)
}
