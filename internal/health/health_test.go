package health

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestHandlerReportsVersionAndPID(t *testing.T) {
	result, err := Handler("1.2.3", "abc123")(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Info
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
	if got.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", got.Version)
	}
	if got.Commit != "abc123" {
		t.Errorf("commit = %q, want abc123", got.Commit)
	}
	if got.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", got.PID, os.Getpid())
	}
}

func TestHandlerJSONFieldNames(t *testing.T) {
	result, _ := Handler("v", "c")(context.Background(), nil)
	encoded, _ := json.Marshal(result)

	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"status", "version", "commit", "pid"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in %s", key, encoded)
		}
	}
}
