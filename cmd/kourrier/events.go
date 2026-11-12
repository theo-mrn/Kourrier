package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/app"
	"github.com/theo-mrn/kourrier/internal/config"
	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/ratelimit"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

const eventsUsage = "usage: kourrier events list [-n N] | replay [-n N] [-id ID] | delete ID [ID...]"

// events implements "kourrier events list|replay|delete" on the event
// dead-letter queue. It uses the same KOURRIER_* configuration as the
// service, so it is meant to run inside a pod:
//
//	kubectl exec deploy/kourrier -- kourrier events list
func events(args []string, w io.Writer) error {
	if len(args) == 0 {
		return errors.New(eventsUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("events "+sub, flag.ContinueOnError)
	fs.SetOutput(w)
	n := fs.Int64("n", 20, "maximum number of entries")
	only := fs.String("id", "", "replay only this entry")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rdb := app.NewRedis(cfg.Redis, 1)
	defer rdb.Close()
	dead := app.NewDeadEvents(cfg, rdb)

	switch sub {
	case "list":
		entries, err := dead.List(ctx, *n)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ENTRY\tTENANT\tSOURCE\tREASON\tROUTES\tFAILED AT\tDETAIL")
		for _, e := range entries {
			routes := "*"
			if len(e.Routes) > 0 {
				routes = fmt.Sprint(e.Routes)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Tenant, e.Source, e.Reason, routes,
				e.FailedAt.Format(time.RFC3339), e.Detail)
		}
		return tw.Flush()
	case "delete":
		if fs.NArg() == 0 {
			return errors.New(eventsUsage)
		}
		if err := dead.Delete(ctx, fs.Args()...); err != nil {
			return err
		}
		fmt.Fprintf(w, "deleted %d entr(ies)\n", fs.NArg())
		return nil
	case "replay":
		return replayEvents(ctx, w, cfg, rdb, dead, *n, *only)
	default:
		return fmt.Errorf("unknown events command %q (%s)", sub, eventsUsage)
	}
}

// replayEvents dispatches dead-lettered events again with the current routes.
// An entry is deleted once replayed; if it fails again, the dispatcher keeps
// it under a new entry. A transient failure stops the replay and keeps the
// entry, so the command can simply be run again.
func replayEvents(ctx context.Context, w io.Writer, cfg *config.Config, rdb redis.UniversalClient,
	dead *deadevent.Store, n int64, only string,
) error {
	if cfg.Events.RoutesFile == "" {
		return errors.New("KOURRIER_EVENTS_ROUTES_FILE is required to replay events")
	}
	tenants, err := tenant.LoadFile(cfg.TenantsFile)
	if err != nil {
		return err
	}
	routes, err := routing.LoadFile(cfg.Events.RoutesFile, tenants)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(io.Discard, "text", "error")
	metrics := observability.NewMetrics(version)
	q, err := app.NewQueue(ctx, cfg, rdb, logger)
	if err != nil {
		return err
	}
	d := dispatch.New(dispatch.Deps{
		Router: routes.Router,
		Submitter: submit.New(submit.Deps{
			Queue:       q,
			Idempotency: idempotency.NewStore(cfg.Redis.KeyPrefix, cfg.Idempotency.TTL),
			Statuses:    status.NewStore(rdb, cfg.Redis.KeyPrefix, cfg.Status.TTL),
			Activity:    app.NewActivity(cfg, rdb),
			Metrics:     metrics, Logger: logger,
		}),
		DeadLetters: dead,
		Limiter:     ratelimit.NewKeyed(rdb, cfg.Redis.KeyPrefix),
		Limits:      routes.Limits,
		Metrics:     metrics, Logger: logger,
	})

	var entries []deadevent.Entry
	if only != "" {
		entries, err = dead.Get(ctx, only)
	} else {
		entries, err = dead.List(ctx, n)
	}
	if err != nil {
		return err
	}
	if only != "" && len(entries) == 0 {
		return fmt.Errorf("no entry %s", only)
	}
	results, err := d.ReplayDead(ctx, dead, tenants, entries)
	var replayed, skipped int
	for _, r := range results {
		switch {
		case r.Skipped != "":
			fmt.Fprintf(w, "%s: skipped (%s)\n", r.EntryID, r.Skipped)
			skipped++
		case r.Outcome == dispatch.Retry:
			fmt.Fprintf(w, "%s: transient failure, kept\n", r.EntryID)
		default:
			replayed++
			line := fmt.Sprintf("%s: %s, %d e-mail(s)", r.EntryID, r.Outcome, r.Emails)
			if r.DeadLetterID != "" {
				line += ", failed again (kept as " + r.DeadLetterID + ")"
			}
			fmt.Fprintln(w, line)
		}
	}
	if err != nil {
		return fmt.Errorf("replay stopped after %d event(s): %w", replayed, err)
	}
	fmt.Fprintf(w, "replayed %d event(s), skipped %d\n", replayed, skipped)
	return nil
}
