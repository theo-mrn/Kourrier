// Command kourrier is an asynchronous, cloud-native e-mailing service.
//
// Usage:
//
//	kourrier [serve]               run the service (API and/or workers, see KOURRIER_MODE)
//	kourrier keygen [-scopes S]    generate an API key and its SHA-256 digest
//	kourrier dlq list [-n N]       list jobs in the dead-letter queue
//	kourrier dlq replay [-n N]     re-enqueue jobs from the dead-letter queue
//	kourrier events list|replay|delete
//	                               inspect and replay the event dead-letter queue
//	kourrier routes check [-tenants FILE] ROUTES
//	                               validate a routes file (for CI)
//	kourrier routes test -tenant ID [-tenants FILE] -event EVENT ROUTES
//	                               show the e-mails a CloudEvent would produce
//	kourrier healthcheck [-addr]   probe the admin /readyz endpoint (for Docker)
//	kourrier version               print the version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/theo-mrn/kourrier/internal/app"
	"github.com/theo-mrn/kourrier/internal/config"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "kourrier:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve()
	case "keygen":
		return keygen(args, stdout)
	case "dlq":
		return dlq(args, stdout)
	case "routes":
		return routes(args, stdout)
	case "events":
		return events(args, stdout)
	case "healthcheck":
		return healthcheck(args)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "usage: kourrier [serve|keygen|dlq list|dlq replay|events list|events replay|events delete|routes check|routes test|healthcheck|version]")
		return nil
	default:
		return fmt.Errorf("unknown command %q (see kourrier help)", cmd)
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, cfg.LogFormat, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, cfg, version, logger)
}

func keygen(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(w)
	scopes := fs.String("scopes", "", "comma-separated scopes: emails, events (default: both)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var list []string
	for _, s := range strings.Split(*scopes, ",") {
		switch s = strings.TrimSpace(s); s {
		case "":
		case tenant.ScopeEmails, tenant.ScopeEvents:
			list = append(list, s)
		default:
			return fmt.Errorf("unknown scope %q (emails, events)", s)
		}
	}
	key, digest, err := tenant.GenerateKey()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "API key (give it to the client application, it is shown only once):\n  %s\n\n", key)
	fmt.Fprintf(w, "Digest (put it in the tenants file):\n  api_keys:\n    - sha256: %s\n", digest)
	if len(list) > 0 {
		fmt.Fprintf(w, "      scopes: [%s]\n", strings.Join(list, ", "))
	}
	return nil
}

func dlq(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kourrier dlq list|replay [-n N]")
	}
	sub := args[0]
	fs := flag.NewFlagSet("dlq "+sub, flag.ContinueOnError)
	n := fs.Int64("n", 20, "maximum number of entries")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rdb := app.NewRedis(cfg.Redis, 1)
	defer rdb.Close()
	q, err := app.NewQueue(ctx, cfg, rdb, observability.NewLogger(io.Discard, "text", "error"))
	if err != nil {
		return err
	}
	entries, err := q.ListDead(ctx, *n)
	if err != nil {
		return err
	}

	switch sub {
	case "list":
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ENTRY\tEMAIL ID\tTENANT\tATTEMPTS\tFAILED AT\tREASON")
		for _, e := range entries {
			emailID, tenantID, attempts := "?", "?", 0
			if e.Job != nil {
				emailID, tenantID, attempts = e.Job.ID, e.Job.TenantID, e.Job.Attempts
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", e.ID, emailID, tenantID, attempts, e.FailedAt, e.Reason)
		}
		return tw.Flush()
	case "replay":
		replayed, err := q.ReplayDead(ctx, entries)
		fmt.Fprintf(w, "replayed %d job(s)\n", replayed)
		return err
	default:
		return fmt.Errorf("unknown dlq command %q", sub)
	}
}

func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	addr := fs.String("addr", "http://127.0.0.1:9090/readyz", "readiness URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *addr, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return fmt.Errorf("not ready (%d): %v", resp.StatusCode, body)
	}
	return nil
}
