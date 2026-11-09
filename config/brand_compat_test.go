package config

import (
	"os"

	"testing"
)

func TestBrandEnvironmentCompatibility(t *testing.T) {
	for _, suffix := range []string{"CONFIG", "DB_PATH", "LLM_MODEL", "NOTIFY_ALLOW_LOCAL", "REPEATER_ALLOW_LOCAL", "EVAL_API_KEY"} {
		t.Run(suffix, func(t *testing.T) {
			name := "prtseek_" + suffix
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ARTEX_"+suffix, "legacy-value")
			if got, source := LookupEnv(suffix); got != "legacy-value" || source != "ARTEX_"+suffix {
				t.Fatalf("legacy alias value=%q source=%q", got, source)
			}
			t.Setenv(name, "new-value")
			if got, source := LookupEnv(suffix); got != "new-value" || source != name {
				t.Fatalf("primary value=%q source=%q", got, source)
			}
			t.Setenv(name, "")
			if got := Env(suffix); got != "" {
				t.Fatal("explicitly empty primary unexpectedly enabled the legacy setting")
			}
		})
	}
}
