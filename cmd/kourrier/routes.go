package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// routes implements "kourrier routes check|test". Both work offline: no Redis,
// no broker, so they can run in the CI pipeline of the repository that holds
// the routes and the templates.
func routes(args []string, w io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kourrier routes check|test [flags] ROUTES_FILE")
	}
	sub := args[0]
	fs := flag.NewFlagSet("routes "+sub, flag.ContinueOnError)
	fs.SetOutput(w)
	tenantsFile := fs.String("tenants", "", "tenants file; enables the tenant and sender checks")
	tenantID := fs.String("tenant", "", "tenant whose routes are evaluated (test)")
	eventFile := fs.String("event", "", "CloudEvent in structured JSON format, - for stdin (test)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: kourrier routes %s [flags] ROUTES_FILE", sub)
	}

	var tenants *tenant.Registry
	var lookup routing.Tenants
	if *tenantsFile != "" {
		var err error
		if tenants, err = tenant.LoadFile(*tenantsFile); err != nil {
			return err
		}
		lookup = tenants
	}
	cfg, err := routing.LoadFile(fs.Arg(0), lookup)
	if err != nil {
		return err
	}

	switch sub {
	case "check":
		fmt.Fprintf(w, "ok: %d route(s), %d NATS source(s)\n", len(cfg.Router.Routes()), len(cfg.NATS))
		if tenants == nil {
			fmt.Fprintln(w, "note: tenants and senders were not checked (use -tenants)")
		}
		return nil
	case "test":
		return routesTest(w, cfg, tenants, *tenantID, *eventFile)
	default:
		return fmt.Errorf("unknown routes command %q", sub)
	}
}

// testOutput is printed by "routes test" for each matching route.
type testOutput struct {
	Route   string `json:"route"`
	Email   any    `json:"email,omitempty"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}

func routesTest(w io.Writer, cfg *routing.Config, tenants *tenant.Registry, tenantID, eventFile string) error {
	if tenantID == "" || eventFile == "" {
		return errors.New("routes test requires -tenant and -event")
	}
	var t *tenant.Tenant
	if tenants != nil {
		var ok bool
		if t, ok = tenants.Get(tenantID); !ok {
			return fmt.Errorf("unknown tenant %q", tenantID)
		}
	}
	var (
		raw []byte
		err error
	)
	if eventFile == "-" {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	} else {
		raw, err = os.ReadFile(eventFile) //nolint:gosec // path given by the operator
	}
	if err != nil {
		return err
	}
	e, err := cloudevent.ParseStructured(raw)
	if err != nil {
		return err
	}
	results, err := cfg.Router.Route(tenantID, e)
	if err != nil {
		return err
	}

	out := make([]testOutput, 0, len(results))
	for _, r := range results {
		o := testOutput{Route: r.Route.Name}
		switch {
		case r.Err != nil:
			o.Error = r.Err.Error()
		case t != nil:
			if err := submit.Prepare(t, r.Request); err != nil {
				o.Error = err.Error()
			}
			o.Email = r.Request
		default:
			o.Email = r.Request
			o.Warning = "not validated against the tenant (use -tenants)"
		}
		out = append(out, o)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	for _, o := range out {
		if o.Error != "" {
			return errors.New("at least one route failed")
		}
	}
	return nil
}
