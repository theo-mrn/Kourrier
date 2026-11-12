package config

import "testing"

func TestDefaultsAreSafe(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mailer.Provider != ProviderConsole {
		t.Fatalf("default mailer must never send real e-mails, got %q", cfg.Mailer.Provider)
	}
	if !cfg.RunsAPI() || !cfg.RunsWorkers() {
		t.Fatal("default mode should run API and workers")
	}
}

func TestValidate(t *testing.T) {
	tests := map[string][2]string{
		"mode":       {"KOURRIER_MODE", "both"},
		"provider":   {"KOURRIER_MAILER_PROVIDER", "sendgrid"},
		"storage":    {"KOURRIER_STORAGE_PROVIDER", "gcs"},
		"visibility": {"KOURRIER_QUEUE_VISIBILITY_TIMEOUT", "10s"},
		"backoff":    {"KOURRIER_WORKER_BACKOFF_BASE", "1h"},
		"duration":   {"KOURRIER_WORKER_SEND_TIMEOUT", "soon"},
	}
	for name, kv := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv(kv[0], kv[1])
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestConsoleValidation(t *testing.T) {
	t.Setenv("KOURRIER_CONSOLE_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("the console must require a token file")
	}
	t.Setenv("KOURRIER_CONSOLE_TOKEN_FILE", "/run/secrets/console-token")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KOURRIER_MODE", "worker")
	if _, err := Load(); err == nil {
		t.Fatal("the console runs with the API only")
	}
}
