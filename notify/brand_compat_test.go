package notify

import (
	"os"
	"testing"
)

func TestBrandNotifyEnvironmentCompatibility(t *testing.T) {
	t.Setenv("prtseek_NOTIFY_ALLOW_LOCAL", "")
	if err := os.Unsetenv("prtseek_NOTIFY_ALLOW_LOCAL"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_NOTIFY_ALLOW_LOCAL", "1")
	if !allowLocalTargets() {
		t.Fatal("legacy notification opt-in lost")
	}
	t.Setenv("prtseek_NOTIFY_ALLOW_LOCAL", "0")
	if allowLocalTargets() {
		t.Fatal("primary disable did not override legacy opt-in")
	}
}
