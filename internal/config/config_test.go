package config

import (
	"os"
	"path/filepath"
	"testing"
)

func validEnv(t *testing.T) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "ticket-key")
	if err := os.WriteFile(keyPath, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUBLIC_WS_BASE_URL", "ws://127.0.0.1:30081/api/v1/realtime")
	t.Setenv("ALLOWED_ORIGINS", "http://127.0.0.1:30080")
	t.Setenv("TICKET_ENCRYPTION_KEY_FILE", keyPath)
	t.Setenv("STORE_MODE", "memory")
}

func TestLoadDefaultsToRedisAndFailsWithoutURL(t *testing.T) {
	validEnv(t)
	t.Setenv("STORE_MODE", "")
	t.Setenv("REDIS_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("default redis mode accepted without REDIS_URL")
	}
}

func TestLoadRejectsAutoAndInvalidTracingEndpoint(t *testing.T) {
	validEnv(t)
	t.Setenv("STORE_MODE", "auto")
	if _, err := Load(); err == nil {
		t.Fatal("obsolete auto store mode accepted")
	}
	validEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector:4317?ticket=secret")
	if _, err := Load(); err == nil {
		t.Fatal("invalid tracing endpoint accepted")
	}
}

func TestLoadRequiresSecurityInputs(t *testing.T) {
	tests := []struct {
		name  string
		clear string
	}{
		{"public URL", "PUBLIC_WS_BASE_URL"}, {"origins", "ALLOWED_ORIGINS"}, {"key path", "TICKET_ENCRYPTION_KEY_FILE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			validEnv(t)
			t.Setenv(tc.clear, "")
			if _, err := Load(); err == nil {
				t.Fatalf("Load succeeded without %s", tc.clear)
			}
		})
	}
}

func TestLoadValidatesURLAndDurations(t *testing.T) {
	validEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicWSBaseURL.Scheme != "ws" || c.TicketTTL >= c.SessionMaxDuration {
		t.Fatalf("unexpected config: %#v", c)
	}
	t.Setenv("ALLOWED_ORIGINS", "*")
	if _, err := Load(); err == nil {
		t.Fatal("wildcard origin accepted")
	}
}

func TestLoadRequiresExactly32RawKeyBytes(t *testing.T) {
	validEnv(t)
	keyPath := filepath.Join(t.TempDir(), "bad-key")
	if err := os.WriteFile(keyPath, []byte("not-a-32-byte-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TICKET_ENCRYPTION_KEY_FILE", keyPath)
	if _, err := Load(); err == nil {
		t.Fatal("non-32-byte key accepted")
	}
}

func TestLoadRequiresWSSForHTTPSOrigin(t *testing.T) {
	validEnv(t)
	t.Setenv("ALLOWED_ORIGINS", "https://console.example.test")
	if _, err := Load(); err == nil {
		t.Fatal("ws URL accepted for HTTPS origin")
	}
}
