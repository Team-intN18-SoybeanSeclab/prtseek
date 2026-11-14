package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestResearchProgressCrossParameterHTTPAndPolling(t *testing.T) {
	memory, err := NewResearchMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &researchRuntime{memory: memory, attempts: map[string]*researchAttempt{}, researchTask: true}
	var note string
	for i := 0; i < 4; i++ {
		out, _ := json.Marshal(httpObservation{Status: 200, SHA256: "same-body", BodyBytes: 0, ElapsedMS: int64(i + 1), Artifact: "changing-file", OriginalURL: "http://localhost/?v=" + string(rune('a'+i))})
		_, note = r.observe(t.Context(), "WebFetch", []byte(`{"v":`+string(rune('0'+i))+`}`), string(out), false)
	}
	if !strings.Contains(note, "跨参数") {
		t.Fatalf("missed stagnation: %s", note)
	}
	state, err := memory.Snapshot()
	if err != nil || len(state.Entries) != 1 {
		t.Fatalf("no persisted checkpoint: %+v %v", state, err)
	}
	for i := 0; i < 10; i++ {
		_, note = r.observe(t.Context(), "wait_agents", []byte(`{}`), "pending", false)
		if strings.Contains(note, "阶段复核") {
			t.Fatal("polling counted as stagnation")
		}
	}
	if r.stageSteps != 4 {
		t.Fatal(r.stageSteps)
	}
}
func TestResearchProgressNovelResultsPeriodicCheckpoint(t *testing.T) {
	memory, err := NewResearchMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &researchRuntime{memory: memory, attempts: map[string]*researchAttempt{}, researchTask: true}
	for i := 0; i < 8; i++ {
		_, note := r.observe(t.Context(), "Bash", []byte(`{}`), string(rune('a'+i)), false)
		if strings.Contains(note, "跨参数") {
			t.Fatal("novel results misclassified")
		}
	}
	state, err := memory.Snapshot()
	if err != nil || len(state.Entries) != 1 {
		t.Fatalf("periodic checkpoint missing: %v", err)
	}
}
func TestResearchExperimentAllocatesDistinctArtifacts(t *testing.T) {
	dir := t.TempDir()
	memory, err := NewResearchMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	tool := researchExperimentTool(memory)
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		res, err := tool.Call(t.Context(), json.RawMessage(`{"hypothesis":"fixture","changed_variable":"single variable","prediction":"new artifact"}`), &actool.ToolContext{WorkingDir: dir})
		if err != nil || res.IsError {
			t.Fatalf("%s %v", res.Flatten(), err)
		}
		var out map[string]string
		if err = json.Unmarshal([]byte(res.Flatten()), &out); err != nil {
			t.Fatal(err)
		}
		if ids[out["experiment_id"]] {
			t.Fatal("reused experiment")
		}
		ids[out["experiment_id"]] = true
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(out["artifact_dir"]))); err != nil || !info.IsDir() {
			t.Fatal("missing directory", err)
		}
	}
	state, err := memory.Snapshot()
	if err != nil || len(state.Entries) != 2 {
		t.Fatalf("missing hypotheses: %v", err)
	}
}
