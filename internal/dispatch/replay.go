package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// ErrReplayInterrupted means a transient failure stopped a replay. The entry
// that failed and the following ones are kept, so the replay can simply be
// run again.
var ErrReplayInterrupted = errors.New("replay interrupted by a transient failure")

// DeadEventDeleter removes replayed entries. *deadevent.Store implements it.
type DeadEventDeleter interface {
	Delete(ctx context.Context, ids ...string) error
}

// TenantLookup finds a tenant by ID. *tenant.Registry implements it.
type TenantLookup interface {
	Get(id string) (*tenant.Tenant, bool)
}

// ReplayResult is the outcome of replaying one kept event.
type ReplayResult struct {
	EntryID string `json:"entry_id"`
	// Skipped explains why an entry was not replayed (no usable event, or a
	// tenant that no longer exists). Skipped entries are kept.
	Skipped string  `json:"skipped,omitempty"`
	Outcome Outcome `json:"outcome,omitempty"`
	Emails  int     `json:"emails"`
	// DeadLetterID is set when the event failed again: it is kept under this
	// new entry.
	DeadLetterID string `json:"dead_letter_id,omitempty"`
}

// ReplayDead dispatches kept events again with the current routes (ADR 015),
// in order. Only the routes that failed are run again. An entry is deleted
// once replayed; if it fails again, the dispatcher keeps it under a new
// entry. A transient failure stops the replay with ErrReplayInterrupted.
//
// The CLI ("kourrier events replay") and the console share this function, so
// that a replay behaves the same wherever it is started from.
func (d *Dispatcher) ReplayDead(ctx context.Context, store DeadEventDeleter, tenants TenantLookup,
	entries []deadevent.Entry,
) ([]ReplayResult, error) {
	results := make([]ReplayResult, 0, len(entries))
	for _, entry := range entries {
		res := ReplayResult{EntryID: entry.ID}
		t, known := tenants.Get(entry.Tenant)
		e, perr := cloudevent.ParseStructured(entry.Event)
		switch {
		case !entry.Replayable() || perr != nil:
			res.Skipped = "not replayable: " + string(entry.Reason)
		case !known:
			res.Skipped = "unknown tenant " + entry.Tenant
		}
		if res.Skipped != "" {
			results = append(results, res)
			continue
		}

		rep := d.Replay(ctx, t, e, entry.Routes)
		res.Outcome, res.Emails, res.DeadLetterID = rep.Outcome, len(rep.Emails), rep.DeadLetterID
		results = append(results, res)
		if rep.Outcome == Retry {
			return results, fmt.Errorf("%w: entry %s: %w", ErrReplayInterrupted, entry.ID, rep.Err)
		}
		if err := store.Delete(ctx, entry.ID); err != nil {
			return results, err
		}
	}
	return results, nil
}
