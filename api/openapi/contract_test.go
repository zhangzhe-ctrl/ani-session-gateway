package openapi

import (
	"os"
	"strings"
	"testing"
)

func TestContractContainsStableCreateSessionSurface(t *testing.T) {
	content, err := os.ReadFile("session-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{"createSession", "idempotencyKey", "workloadKind", "exec:", "vmConsole:", "connectUrl", "'409'", "'422'", "'429'", "'503'"} {
		if !strings.Contains(text, required) {
			t.Errorf("OpenAPI contract missing %q", required)
		}
	}
}
