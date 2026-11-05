// Package mapping turns source change batches into target operations. The
// New API → ACG rules are defined later (design-core §1.1, stage four); the
// framework only depends on the Mapper interface.
package mapping

import (
	"context"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

// Mapper converts one batch into ordered target operations. Order matters:
// the pipeline applies operations in the returned order, so dependencies
// (for example a group before its users) must come first. Errors are treated
// as permanent: a mapping that fails will fail again on retry.
type Mapper interface {
	Transform(ctx context.Context, batch event.ChangeBatch) ([]target.TargetOperation, error)
}
