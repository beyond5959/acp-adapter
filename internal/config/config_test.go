package config

import (
	"reflect"
	"testing"
)

func TestDefaultCodexAppServerArgs(t *testing.T) {
	t.Parallel()

	want := []string{"app-server", "-c", `model_reasoning_summary="detailed"`}
	if got := DefaultCodexAppServerArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultCodexAppServerArgs() = %v, want %v", got, want)
	}
}
