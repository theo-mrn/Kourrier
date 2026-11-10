package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/theo-mrn/kourrier/internal/config"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// TestEndToEndEvents publishes a CloudEvent to NATS JetStream and to the
// HTTP sink, and checks that both end up as rendered e-mails.
func TestEndToEndEvents(t *testing.T) {
	mr := miniredis.RunT(t)
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, _ := jetstream.New(nc)
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{Name: "USERS", Subjects: []string{"users.>"}}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	key, digest, _ := tenant.GenerateKey()
	write := func(path, content string) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tenants.yaml", `
tenants:
  - id: yner
    api_keys: [{sha256: `+digest+`}]
    default_sender: {email: no-reply@yner.fr}
    allowed_senders: [no-reply@yner.fr]
`)
	write("routes.yaml", `
apiVersion: kourrier/v1alpha1
sources:
  nats:
    - { name: users, tenant: yner, stream: USERS, consumer: kourrier }
routes:
  - name: welcome
    tenant: yner
    match: { types: [com.yner.user.created] }
    email:
      template: welcome
      to: data.email
      data: '{"name": data.first_name}'
`)
	write("templates/yner/welcome/en/subject.txt", "Welcome {{.name}}")
	write("templates/yner/welcome/en/body.txt", "Hello {{.name}}")

	cfg := testConfig(t)
	cfg.TenantsFile = filepath.Join(dir, "tenants.yaml")
	cfg.Redis.Addr = mr.Addr()
	cfg.Storage.Provider = config.StorageFilesystem
	cfg.Storage.Dir = filepath.Join(dir, "templates")
	cfg.Mailer.Provider = config.ProviderFilesystem
	cfg.Mailer.Filesystem.Dir = filepath.Join(dir, "outbox")
	cfg.Events.RoutesFile = filepath.Join(dir, "routes.yaml")
	cfg.Events.NATS.URL = ns.ClientURL()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned %v", err)
		}
	})
	waitReady(t, "http://"+cfg.AdminAddr+"/readyz")
	// The consumer only receives events published after its creation.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := js.Consumer(context.Background(), "USERS", "kourrier"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("consumer not created")
		}
	}

	if _, err := js.PublishMsg(context.Background(), &nats.Msg{
		Subject: "users.created",
		Header:  nats.Header{"Content-Type": []string{"application/cloudevents+json"}},
		Data: []byte(`{"specversion":"1.0","id":"u-1","source":"/users","type":"com.yner.user.created",
			"data":{"email":"jane@example.com","first_name":"Jane"}}`),
	}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://"+cfg.HTTPAddr+"/v1/events", strings.NewReader(`{"email":"john@example.com","first_name":"John"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range map[string]string{"Ce-Specversion": "1.0", "Ce-Id": "u-2", "Ce-Source": "/users", "Ce-Type": "com.yner.user.created"} {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /v1/events status = %d", resp.StatusCode)
	}

	deadline := time.Now().Add(15 * time.Second)
	var bodies []string
	for time.Now().Before(deadline) && len(bodies) < 2 {
		time.Sleep(50 * time.Millisecond)
		files, _ := filepath.Glob(filepath.Join(dir, "outbox", "*.txt"))
		bodies = bodies[:0]
		for _, f := range files {
			b, _ := os.ReadFile(f)
			bodies = append(bodies, string(b))
		}
	}
	joined := strings.Join(bodies, "|")
	if !strings.Contains(joined, "Hello Jane") || !strings.Contains(joined, "Hello John") {
		t.Fatalf("expected both e-mails in the outbox, got %q", joined)
	}

	metrics := get(t, "http://"+cfg.AdminAddr+"/metrics")
	for _, want := range []string{
		`kourrier_events_received_total{outcome="accepted",source="users"} 1`,
		`kourrier_events_received_total{outcome="accepted",source="http"} 1`,
		`kourrier_event_routes_total{outcome="queued",route="welcome",tenant="yner"} 2`,
		`kourrier_event_source_up{source="users"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
}
