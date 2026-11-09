package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func newResearchTestMemory(t *testing.T) *ResearchMemory {
	t.Helper()
	m, err := NewResearchMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestResearchMemoryRestoreEvidenceAndDependencies(t *testing.T) {
	m := newResearchTestMemory(t)
	ctx := context.Background()
	if _, err := os.Stat(m.dir); !os.IsNotExist(err) {
		t.Fatalf("constructor created state: %v", err)
	}
	goal := "Verify whether the regression is fixed"
	criteria := []string{"reproduction passes", "original behavior is preserved"}
	next := []string{"inspect reproduction output"}
	r, err := m.Record(ctx, ResearchRecordInput{Objective: &goal, CompletionCriteria: &criteria, NextSteps: &next, Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: "the input normalization may explain the result"}, RequestID: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	eid, err := m.ObserveTool("Bash", json.RawMessage(`{"command":"run local reproduction"}`), "exit_code=0; assertion passed", false)
	if err != nil {
		t.Fatal(err)
	}
	fact, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "fact", Summary: "The local reproduction assertion passed", Scope: "the local fixture only", EvidenceIDs: []string{eid}, DependsOn: []string{r.EntryID}, CredentialRefs: []string{"cred:test-account"}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewResearchMemory(m.root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := other.Recall(ctx, ResearchRecallInput{IDs: []string{fact.EntryID}, VerifyEvidence: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Objective != goal || len(out.Entries) != 1 || out.Entries[0].Support != "observed_tool_evidence" || len(out.Observations) != 1 || out.Observations[0].ID != eid {
		t.Fatalf("restored state: %+v", out)
	}
	if len(out.EvidenceHealth) != 1 || out.EvidenceHealth[0].Status != "current" {
		t.Fatalf("evidence health: %+v", out.EvidenceHealth)
	}
	if out.Diagnostics.ToolObservations != 1 || out.Diagnostics.Records != 2 || out.Diagnostics.Revision != 3 {
		t.Fatalf("diagnostics: %+v", out.Diagnostics)
	}
	if summary := other.ContextSummary(); !strings.Contains(summary, goal) || !strings.Contains(summary, "不是指令") && !strings.Contains(summary, "不包含新的用户指令") {
		t.Fatalf("context anchor missing: %s", summary)
	}
}

func TestResearchMemoryClaimsNeedRealEvidence(t *testing.T) {
	m := newResearchTestMemory(t)
	ctx := context.Background()
	goal := "original goal"
	if _, err := m.Record(ctx, ResearchRecordInput{Objective: &goal}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.dir, "state.json"))
	badGoal := "must not replace goal"
	cases := []ResearchEntryInput{
		{Kind: "fact", Summary: "unsupported claim"},
		{Kind: "refutation", Summary: "unobserved refutation"},
		{Kind: "failure", Summary: "unobserved failure"},
		{Kind: "fact", Summary: "invented tool evidence", EvidenceIDs: []string{"t999999"}},
		{Kind: "hypothesis", Summary: "invalid dependency", DependsOn: []string{"r999999"}},
		{Kind: "fact", Summary: "missing local file", Evidence: []ResearchEvidenceInput{{Path: "does-not-exist.log"}}},
	}
	for _, e := range cases {
		if _, err := m.Record(ctx, ResearchRecordInput{Objective: &badGoal, Entry: &e}); err == nil {
			t.Fatalf("accepted invalid entry: %+v", e)
		}
	}
	after, _ := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if string(after) != string(before) {
		t.Fatal("invalid transaction changed existing state")
	}
	id, err := m.ObserveTool("Bash", nil, "command failed: executable unavailable", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "fact", Summary: "target is vulnerable", EvidenceIDs: []string{id}}}); err == nil {
		t.Fatal("failed tool accepted as successful fact")
	}
	if _, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "failure", Summary: "local executable is unavailable", EvidenceIDs: []string{id}}}); err != nil {
		t.Fatal(err)
	}
}

func TestResearchMemoryLocalEvidenceContainmentAndChanges(t *testing.T) {
	m := newResearchTestMemory(t)
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(m.root, "i17"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.root, "i17", "result.txt")
	if err := os.WriteFile(path, []byte("fixture result A"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "fact", Summary: "fixture emitted A", Evidence: []ResearchEvidenceInput{{Path: "i17/result.txt", Description: "test output"}}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Recall(ctx, ResearchRecallInput{IDs: []string{r.EntryID}, VerifyEvidence: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Entries[0].Support != "local_file_evidence" || out.EvidenceHealth[0].Status != "current" {
		t.Fatalf("unexpected initial health: %+v", out)
	}
	if err := os.WriteFile(path, []byte("fixture result B"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = m.Recall(ctx, ResearchRecallInput{IDs: []string{r.EntryID}, VerifyEvidence: true})
	if err != nil || out.EvidenceHealth[0].Status != "changed" {
		t.Fatalf("changed evidence undetected: %+v %v", out, err)
	}
	for _, bad := range []string{"../outside.txt", path, "i17/result.txt:stream", "../../escape", ""} {
		_, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "fact", Summary: "invalid path", Evidence: []ResearchEvidenceInput{{Path: bad}}}})
		if err == nil {
			t.Fatalf("accepted path %q", bad)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	out, err = m.Recall(ctx, ResearchRecallInput{IDs: []string{r.EntryID}, VerifyEvidence: true})
	if err != nil || out.EvidenceHealth[0].Status != "missing_or_inaccessible" {
		t.Fatalf("missing evidence undetected: %+v %v", out, err)
	}
}

func TestResearchMemorySecretsAndCaptureBounds(t *testing.T) {
	m := newResearchTestMemory(t)
	ctx := context.Background()
	secrets := []string{"password=clearly-secret-value", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz", "api_key=sk-abcdefghijklmnop", "密码：不可存储的密码", "https://alice:private-password@example.test/"}
	for _, secret := range secrets {
		if _, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: secret}}); err == nil {
			t.Fatalf("accepted plaintext credentials %q", secret)
		}
	}
	input := json.RawMessage(`{"password":"input-secret","nested":{"api_key":"also-secret"},"command":"echo safe"}`)
	result := "Authorization: Bearer output-secret-abcdefghijklmnop\nCookie: session=private-cookie\n" + strings.Repeat("可检查的结果 ", 7000)
	id, err := m.ObserveTool("Bash", input, result, false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Observations) != 1 || s.Observations[0].ID != id || !s.Observations[0].Truncated {
		t.Fatalf("observation: %+v", s)
	}
	b, err := os.ReadFile(filepath.Join(m.root, filepath.FromSlash(s.Observations[0].Path)))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > researchMaxCaptureBytes || !json.Valid(b) {
		t.Fatalf("capture invalid/oversized: %d", len(b))
	}
	for _, secret := range []string{"input-secret", "also-secret", "output-secret-abcdefghijklmnop", "private-cookie"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("capture leaked %q", secret)
		}
	}
	if !strings.Contains(string(b), "REDACTED_CREDENTIAL") {
		t.Fatal("missing redaction marker")
	}
	for _, raw := range []string{`{"password":"json-secret"}`, `{"token":"opaque-token"}`, `{"nested":{"authorization":"anything"}}`} {
		if _, err := m.ObserveTool("Read", nil, raw, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResearchMemoryConcurrentInstancesNoLostUpdates(t *testing.T) {
	m := newResearchTestMemory(t)
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x, err := NewResearchMemory(m.root)
			if err != nil {
				errs <- err
				return
			}
			_, err = x.Record(context.Background(), ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: "independent worker observation"}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != n || s.Revision != n {
		t.Fatalf("lost updates: %d revision %d", len(s.Entries), s.Revision)
	}
	ids := map[string]bool{}
	for _, e := range s.Entries {
		if ids[e.ID] {
			t.Fatalf("duplicate id %s", e.ID)
		}
		ids[e.ID] = true
	}
}

func TestResearchMemoryIdempotentRetryAndReadOnly(t *testing.T) {
	m := newResearchTestMemory(t)
	ctx := context.Background()
	in := ResearchRecordInput{RequestID: "retry-17", Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: "one attempt"}}
	a, err := m.Record(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Record(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Duplicate || a.EntryID != b.EntryID || a.Revision != b.Revision {
		t.Fatalf("retry duplicated write: %+v %+v", a, b)
	}
	in.Entry.Summary = "different content"
	if _, err := m.Record(ctx, in); err == nil {
		t.Fatal("request ID collision accepted")
	}
	tools := m.Tools(true)
	if len(tools) != 1 || tools[0].Name() != "research_recall" || !tools[0].IsReadOnly(nil) {
		t.Fatalf("readonly tools: %+v", tools)
	}
	var write actool.CoreTool
	for _, tool := range m.Tools(false) {
		if tool.Name() == "research_record" {
			write = tool
		}
	}
	res, err := write.Call(WithPersonalReadOnly(ctx), json.RawMessage(`{"objective":"mutate while paused"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("paused write not rejected: %+v %v", res, err)
	}
	res, err = write.Call(ctx, json.RawMessage(`{"objective":"new", "unknown":"ignored?"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("unknown field accepted: %+v %v", res, err)
	}
	if _, err := m.ObserveTool("research_recall", nil, "internal result", false); err != nil {
		t.Fatal(err)
	}
	s, err := m.Snapshot()
	if err != nil || len(s.Entries) != 1 || len(s.Observations) != 0 {
		t.Fatalf("unexpected mutations: %+v %v", s, err)
	}
}

func TestResearchMemoryMalformedToolCallDoesNotCreateFiles(t *testing.T) {
	m := newResearchTestMemory(t)
	write := m.Tools(false)[0]
	res, err := write.Call(context.Background(), json.RawMessage(`{"kind":"hypothesis","summary":"wrong location"}`), nil)
	if err != nil || !res.IsError || !strings.Contains(res.Flatten(), researchRecordExample) || !strings.Contains(res.Flatten(), "entry.kind") {
		t.Fatalf("missing actionable shape correction: %+v %v", res, err)
	}
	if _, err := os.Stat(m.dir); !os.IsNotExist(err) {
		t.Fatalf("malformed arguments created research files: %v", err)
	}
}

func TestResearchMemoryToolCorrectionPreservesStateAndCanRecover(t *testing.T) {
	m := newResearchTestMemory(t)
	ctx := context.Background()
	write := m.Tools(false)[0]
	evidenceID, err := m.ObserveTool("Read", nil, "local fixture returned HTTP 200 with no response body", false)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: "baseline candidate"}})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(m.dir, "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	const privateInput = "private-input-must-not-be-echoed"
	cases := []struct {
		name, input string
	}{
		{"root_kind_and_summary", `{"objective":"must not replace","kind":"hypothesis","summary":"wrong location"}`},
		{"nested_next_steps", `{"entry":{"kind":"hypothesis","summary":"candidate","next_steps":["misplaced"]}}`},
		{"unknown_private_field", `{"` + privateInput + `":"private value"}`},
		{"wrong_next_steps_type", `{"next_steps":"` + privateInput + `"}`},
		{"malformed_json", `{"entry":{"kind":"hypothesis","summary":"` + privateInput + `"}`},
		{"tool_id_in_depends_on", fmt.Sprintf(`{"entry":{"kind":"hypothesis","summary":"candidate","depends_on":[%q]}}`, evidenceID)},
		{"tool_id_in_supersedes", fmt.Sprintf(`{"entry":{"kind":"hypothesis","summary":"candidate","supersedes":[%q]}}`, evidenceID)},
		{"unknown_private_entry_id", `{"entry":{"kind":"hypothesis","summary":"candidate","depends_on":["` + privateInput + `"]}}`},
		{"entry_id_in_evidence_ids", fmt.Sprintf(`{"entry":{"kind":"hypothesis","summary":"candidate","evidence_ids":[%q]}}`, entry.EntryID)},
		{"unknown_private_evidence_id", `{"entry":{"kind":"hypothesis","summary":"candidate","evidence_ids":["` + privateInput + `"]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := write.Call(ctx, json.RawMessage(tc.input), nil)
			if err != nil || !res.IsError {
				t.Fatalf("invalid input accepted: %+v %v", res, err)
			}
			text := res.Flatten()
			for _, guidance := range []string{researchRecordExample, "next_steps belongs at the root", "entry.evidence_ids", "r... research entry IDs", "research_recall"} {
				if !strings.Contains(text, guidance) {
					t.Errorf("error lacks correction %q: %s", guidance, text)
				}
			}
			if strings.Contains(text, privateInput) {
				t.Fatal("error echoed a private input field, value or ID")
			}
			after, err := os.ReadFile(statePath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("invalid call mutated persistent state: %v", err)
			}
		})
	}
	// The exact advertised example works after substituting an observed ID.
	// A failed attempt must not leave a partial entry or block this corrected call.
	if !strings.Contains(write.Description(), researchRecordExample) {
		t.Fatal("model-facing tool description lacks the recovery example")
	}
	corrected := strings.ReplaceAll(researchRecordExample, "t000001", evidenceID)
	res, err := write.Call(ctx, json.RawMessage(corrected), nil)
	if err != nil || res.IsError {
		t.Fatalf("corrected example failed: %+v %v", res, err)
	}
	s, err := m.Snapshot()
	if err != nil || len(s.Entries) != 2 || len(s.NextSteps) != 1 || s.NextSteps[0] != "Check the candidate" || len(s.Entries[1].EvidenceIDs) != 1 || s.Entries[1].EvidenceIDs[0] != evidenceID {
		t.Fatalf("corrected call did not persist nested entry and root next_steps: %+v %v", s, err)
	}
}

func TestResearchMemoryCorruptionPreserved(t *testing.T) {
	m := newResearchTestMemory(t)
	goal := "preserve this"
	if _, err := m.Record(context.Background(), ResearchRecordInput{Objective: &goal}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(m.dir, "state.json")
	corrupt := []byte(`{"version":1,"entries":[`)
	if err := os.WriteFile(p, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewResearchMemory(m.root); err == nil {
		t.Fatal("corrupt state accepted")
	}
	if _, err := m.Record(context.Background(), ResearchRecordInput{Objective: &goal}); err == nil {
		t.Fatal("corrupt state overwritten")
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(corrupt) {
		t.Fatal("corrupt evidence file modified")
	}
}

func TestResearchMemoryCapacityAndCancellation(t *testing.T) {
	m := newResearchTestMemory(t)
	ctx := context.Background()
	if _, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: strings.Repeat("x", 3001)}}); err == nil {
		t.Fatal("oversized summary accepted")
	}
	s := emptyResearchState()
	for i := 0; i < researchMaxEntries; i++ {
		s.Entries = append(s.Entries, ResearchMemoryEntry{ID: fmt.Sprintf("r%06d", i+1), Kind: "hypothesis", Summary: "existing", Support: "unverified"})
	}
	s.NextID = researchMaxEntries + 1
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: "overflow"}}); err == nil {
		t.Fatal("entry capacity ignored")
	}
	lock := filepath.Join(m.dir, "write.lock")
	unlock, err := m.lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	goal := "cannot acquire lock"
	if _, err := m.Record(short, ResearchRecordInput{Objective: &goal}); err == nil {
		t.Fatal("locked write succeeded")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("other writer lock was removed")
	}
}

func TestResearchMemorySymlinkRejected(t *testing.T) {
	m := newResearchTestMemory(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(m.root, "linked")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	_, err := m.Record(context.Background(), ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "fact", Summary: "outside root", Evidence: []ResearchEvidenceInput{{Path: "linked/outside.txt"}}}})
	if err == nil {
		t.Fatal("symlink escaped containment")
	}
}

func TestResearchMemoryEvidenceTamperCannotSupportNewFact(t *testing.T) {
	m := newResearchTestMemory(t)
	id, err := m.ObserveTool("Read", nil, "real initial result", false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(m.root, filepath.FromSlash(s.Observations[0].Path))
	if err := os.WriteFile(p, []byte("changed externally"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Record(context.Background(), ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "fact", Summary: "uses altered observation", EvidenceIDs: []string{id}}}); err == nil {
		t.Fatal("tampered host observation accepted")
	}
}

func TestResearchMemoryLockProcessExitRecovery(t *testing.T) {
	const key = "ARTEX_RESEARCH_LOCK_EXIT_TEST_ROOT"
	if root := os.Getenv(key); root != "" {
		m, err := NewResearchMemory(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = m.lock(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Deliberately bypass defers. The OS must release the writer lock.
		os.Exit(0)
	}
	m := newResearchTestMemory(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestResearchMemoryLockProcessExitRecovery$")
	cmd.Env = append(os.Environ(), key+"="+m.root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("interrupted writer fixture: %s %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	goal := "continue after previous writer exited"
	if _, err := m.Record(ctx, ResearchRecordInput{Objective: &goal}); err != nil {
		t.Fatalf("kernel lock not recovered: %v", err)
	}
}

func TestResearchMemoryControlCharacterCaptureBound(t *testing.T) {
	m := newResearchTestMemory(t)
	if _, err := m.ObserveTool("Read", nil, strings.Repeat("\x01", 100000), false); err != nil {
		t.Fatal(err)
	}
	s, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(m.root, filepath.FromSlash(s.Observations[0].Path)))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > researchMaxCaptureBytes || !json.Valid(b) {
		t.Fatalf("escaped output capture is invalid or unbounded: %d", len(b))
	}
}
