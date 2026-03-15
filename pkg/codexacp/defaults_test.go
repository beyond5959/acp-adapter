package codexacp

import (
	"reflect"
	"testing"
)

func TestDefaultRuntimeConfigEnablesDetailedReasoningSummary(t *testing.T) {
	t.Parallel()

	cfg := DefaultRuntimeConfig()
	want := []string{"app-server", "-c", `model_reasoning_summary="detailed"`}
	if !reflect.DeepEqual(cfg.AppServerArgs, want) {
		t.Fatalf("DefaultRuntimeConfig().AppServerArgs = %v, want %v", cfg.AppServerArgs, want)
	}
}
