package agent

import (
	"strings"
	"testing"
)

func TestCTFRoleMethodsStayDistinct(t *testing.T) {
	for _, tc := range []struct{ key, marker string }{
		{"triage", "材料清点"},
		{"command", "停止条件"},
		{"solver", "Crypto"},
		{"writeup", "EXP"},
	} {
		prompt := chatSystem(tc.key, t.TempDir(), t.TempDir())
		if !strings.Contains(prompt, CTFEvidencePriority) || !strings.Contains(prompt, tc.marker) {
			t.Errorf("%s missing CTF method", tc.key)
		}
	}
	if ctfRoleMethod("ctf_verifier") != "" {
		t.Fatal("retired verifier regained a method card")
	}
}
