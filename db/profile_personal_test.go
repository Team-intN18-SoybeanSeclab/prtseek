package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func personalProfileFixture(t *testing.T, d *DB) (*LLMProfile, *Conversation) {
	t.Helper()
	p := &LLMProfile{Name: fmt.Sprintf("personal-profile-%d", time.Now().UnixNano()), Format: "openai", Model: "original-model", APIKey: "fixture-original-key"}
	id, err := d.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = id
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, p.ID) })
	return p, personalTestConversation(t, d)
}

func TestPersonalProfileMutationsProtectActiveSnapshot(t *testing.T) {
	for _, status := range []string{"created", "running", "paused", "done", "failed", "timeout"} {
		t.Run(status, func(t *testing.T) {
			d := personalTestDB(t)
			p, c := personalProfileFixture(t, d)
			h := personalTestHandoff(c, "profile-guard")
			h.ConfigSnapshot, _ = json.Marshal(map[string]any{"profile_id": p.ID})
			task, err := d.CreateTaskWithOptions("profile guard", "local database fixture", TaskCreateOptions{LLMProfileIDs: []int64{p.ID}, PersonalHandoff: h})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(`UPDATE tasks SET status=$2,paused=$3 WHERE id=$1`, task.ID, status, status == "paused"); err != nil {
				t.Fatal(err)
			}
			p.Name = "attempted edit"
			p.APIKey = "fixture-replacement-key"
			_, editErr := d.SaveProfile(p)
			deleteErr := d.DeleteProfile(p.ID)
			if !IsTerminal(status) {
				if !errors.Is(editErr, ErrLLMProfileBackgroundInUse) || !errors.Is(deleteErr, ErrLLMProfileBackgroundInUse) {
					t.Fatalf("active %s snapshot not protected: edit=%v delete=%v", status, editErr, deleteErr)
				}
				stored, err := d.ProfileByID(p.ID)
				if err != nil || stored == nil || stored.APIKey != "fixture-original-key" || stored.Model != "original-model" {
					t.Fatal("rejected edit changed the persisted profile")
				}
			} else if editErr != nil || deleteErr != nil {
				t.Fatalf("stale active flag on terminal %s task blocked a profile: edit=%v delete=%v", status, editErr, deleteErr)
			}
		})
	}
}

func TestPersonalProfileOrdinaryConversationAllowsNextRoundEdit(t *testing.T) {
	d := personalTestDB(t)
	p, c := personalProfileFixture(t, d)
	if err := d.UpdateConversationProfile(c.ID, &p.ID); err != nil {
		t.Fatal(err)
	}
	p.Name = "ordinary conversation can edit"
	p.APIKey = ""
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	stored, err := d.ProfileByID(p.ID)
	if err != nil || stored == nil || stored.Name != p.Name || stored.APIKey != "fixture-original-key" {
		t.Fatal("ordinary edit did not preserve the stored key")
	}
}

func TestPersonalProfileMutationWaitsForHandoffCommit(t *testing.T) {
	for _, mutation := range []string{"save", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			d := personalTestDB(t)
			p, c := personalProfileFixture(t, d)
			task, err := d.CreateTaskWithOptions("uncommitted profile guard", "local database fixture", TaskCreateOptions{LLMProfileIDs: []int64{p.ID}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
			tx, err := d.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`SELECT id FROM llm_profiles WHERE id=$1`, p.ID); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := json.Marshal(map[string]any{"profile_id": p.ID})
			if _, err := tx.Exec(`INSERT INTO conversation_task_runs(conversation_id,task_id,idempotency_key,checkpoint,config_snapshot) VALUES($1,$2,'uncommitted','fixture',$3)`, c.ID, task.ID, string(snapshot)); err != nil {
				t.Fatal(err)
			}
			writer, pid := openSingleConnectionTestDB(t, testDSN(t))
			result := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			go func() {
				if mutation == "save" {
					_, err := writer.SaveProfileContext(ctx, p)
					result <- err
				} else {
					result <- writer.DeleteProfileContext(ctx, p.ID)
				}
			}()
			if err := waitForBackendBlock(d, pid, result); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := waitForMutationResult(result); !errors.Is(err, ErrLLMProfileBackgroundInUse) {
				t.Fatalf("%s did not recheck the newly committed snapshot: %v", mutation, err)
			}
		})
	}
}
