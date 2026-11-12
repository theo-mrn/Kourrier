package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/theo-mrn/kourrier/internal/config"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// TestEndToEnd boots the whole service (API + workers) against an in-memory
// Redis, sends an e-mail through the HTTP API and checks that the filesystem
// driver received the rendered message.
func TestEndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)
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
    default_sender: {email: no-reply@yner.fr, name: Yner}
    allowed_senders: [no-reply@yner.fr]
`)
	write("templates/yner/welcome/fr/subject.txt", "Bienvenue {{.name}}")
	write("templates/yner/welcome/fr/body.html", "<h1>Bonjour {{.name}}</h1>")

	cfg := testConfig(t)
	cfg.TenantsFile = filepath.Join(dir, "tenants.yaml")
	cfg.Redis.Addr = mr.Addr()
	cfg.Storage.Provider = config.StorageFilesystem
	cfg.Storage.Dir = filepath.Join(dir, "templates")
	cfg.Mailer.Provider = config.ProviderFilesystem
	cfg.Mailer.Filesystem.Dir = filepath.Join(dir, "outbox")
	const consoleToken = "end-to-end-console-token-0123456789"
	write("console-token", consoleToken+"\n")
	cfg.Console.Enabled = true
	cfg.Console.Addr = freeAddr(t)
	cfg.Console.TokenFile = filepath.Join(dir, "console-token")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned %v", err)
		}
	})

	base := "http://" + cfg.HTTPAddr
	waitReady(t, "http://"+cfg.AdminAddr+"/readyz")

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/emails", strings.NewReader(
		`{"to":[{"email":"jane@example.com"}],"template":{"name":"welcome","locale":"fr","data":{"name":"Jane"}}}`))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&accepted)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}

	// Wait for the worker to deliver the e-mail.
	var state string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && state != "sent" {
		time.Sleep(50 * time.Millisecond)
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/emails/"+accepted.ID, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var st struct{ Status string }
		_ = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		state = st.Status
	}
	if state != "sent" {
		t.Fatalf("e-mail not sent in time, last status %q", state)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "outbox", "*_"+accepted.ID+".html"))
	if len(files) != 1 {
		t.Fatalf("expected one rendered e-mail in the outbox, found %v", files)
	}
	html, _ := os.ReadFile(files[0])
	if string(html) != "<h1>Bonjour Jane</h1>" {
		t.Fatalf("unexpected HTML %q", html)
	}

	// The console signs in with the admin token and shows the activity of
	// the e-mail: queued by the API, then sent by the worker.
	console := "http://" + cfg.Console.Addr
	login, _ := http.NewRequest(http.MethodPost, console+"/api/session", strings.NewReader(`{"token":"`+consoleToken+`"}`))
	login.Header.Set("X-Kourrier-Console", "1")
	resp, err = http.DefaultClient.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 1 {
		t.Fatalf("console sign-in: %d", resp.StatusCode)
	}
	activityReq, _ := http.NewRequest(http.MethodGet, console+"/api/activity?email="+accepted.ID, nil)
	activityReq.AddCookie(resp.Cookies()[0])
	resp, err = http.DefaultClient.Do(activityReq)
	if err != nil {
		t.Fatal(err)
	}
	var feed struct{ Entries []struct{ State string } }
	_ = json.NewDecoder(resp.Body).Decode(&feed)
	resp.Body.Close()
	if len(feed.Entries) != 2 || feed.Entries[0].State != "sent" || feed.Entries[1].State != "queued" {
		t.Fatalf("console activity = %+v", feed)
	}

	metrics := get(t, "http://"+cfg.AdminAddr+"/metrics")
	for _, want := range []string{
		`kourrier_emails_sent_total{provider="filesystem",tenant="yner"} 1`,
		`kourrier_http_requests_total{code="202",method="POST",route="/v1/emails"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("KOURRIER_HTTP_ADDR", freeAddr(t))
	t.Setenv("KOURRIER_ADMIN_ADDR", freeAddr(t))
	t.Setenv("KOURRIER_RATE_LIMIT_SCOPE", "local")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Queue.SchedulerInterval = 50 * time.Millisecond
	cfg.ShutdownTimeout = 5 * time.Second
	return cfg
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("service did not become ready")
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
