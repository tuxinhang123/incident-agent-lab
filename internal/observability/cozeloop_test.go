package observability

import (
	"os"
	"testing"
)

func TestNewDisabledWithoutEnv(t *testing.T) {
	os.Unsetenv(EnvAPIToken)
	os.Unsetenv(EnvWorkspaceID)
	if tr := New(); tr != nil {
		t.Fatalf("tracer must be nil without credentials, got %T", tr)
	}
}

func TestNewDisabledWithOnlyToken(t *testing.T) {
	t.Setenv(EnvAPIToken, "pat-test")
	os.Unsetenv(EnvWorkspaceID)
	if tr := New(); tr != nil {
		t.Fatalf("tracer requires both token and workspace id, got %T", tr)
	}
}
