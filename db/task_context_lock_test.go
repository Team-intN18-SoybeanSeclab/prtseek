package db

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestTaskLLMProfileMutationsLockTaskBeforeProfile(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("SQLite unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	first, err := d.SaveProfile(&LLMProfile{
		Name: fmt.Sprintf("lock-order-first-%d", suffix), Format: "openai", Model: "first", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.SaveProfile(&LLMProfile{
		Name: fmt.Sprintf("lock-order-second-%d", suffix), Format: "openai", Model: "second", APIKey: "test-key",
	})
	if err != nil {
		_ = d.DeleteProfile(first)
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("LLM lock order", "verify concurrent mutation locks", TaskCreateOptions{
		LLMProfileIDs: []int64{first, second},
	})
	if err != nil {
		_ = d.DeleteProfile(first)
		_ = d.DeleteProfile(second)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_ = d.DeleteProfile(first)
		_ = d.DeleteProfile(second)
	})

	replaceDB, replacePID := openSingleConnectionTestDB(t, dsn)
	replaceBlocker, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer replaceBlocker.Rollback()
	if _, err := replaceBlocker.Exec(`SELECT id FROM tasks WHERE id=$1`, task.ID); err != nil {
		replaceBlocker.Rollback()
		t.Fatal(err)
	}
	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- replaceDB.ReplaceTaskLLMProfiles(task.ID, []int64{second, first}, second)
	}()
	if err := waitForBackendBlock(d, replacePID, replaceDone); err != nil {
		replaceBlocker.Rollback()
		t.Fatal(err)
	}
	assertProfilesUnlocked(t, d, first, second)
	if err := replaceBlocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := waitForMutationResult(replaceDone); err != nil {
		t.Fatalf("replace chain: %v", err)
	}

	deleteDB, deletePID := openSingleConnectionTestDB(t, dsn)
	deleteBlocker, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer deleteBlocker.Rollback()
	if _, err := deleteBlocker.Exec(`SELECT id FROM tasks WHERE id=$1`, task.ID); err != nil {
		deleteBlocker.Rollback()
		t.Fatal(err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- deleteDB.DeleteProfile(second)
	}()
	if err := waitForBackendBlock(d, deletePID, deleteDone); err != nil {
		deleteBlocker.Rollback()
		t.Fatal(err)
	}
	assertProfilesUnlocked(t, d, second)
	if err := deleteBlocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := waitForMutationResult(deleteDone); err != nil {
		t.Fatalf("delete profile: %v", err)
	}

	got, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID == nil || *got.ActiveLLMProfileID != first {
		t.Fatalf("deleting the active profile did not select its successor: %+v", got)
	}
}

func TestDeleteProfileLocksNonTaskReferencesBeforeProfile(t *testing.T) {
	dsn := testDSN(t)

	t.Run("agent", func(t *testing.T) {
		d, err := Open(dsn)
		if err != nil {
			t.Fatalf("SQLite unavailable (%v) - skipping", err)
		}
		defer d.Close()

		suffix := time.Now().UnixNano()
		profileID, err := d.SaveProfile(&LLMProfile{
			Name: fmt.Sprintf("agent-lock-profile-%d", suffix), Format: "openai", Model: "agent-lock", APIKey: "test-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		agent, err := d.CreateAgent(fmt.Sprintf("lock_agent_%d", suffix), "lock agent", "")
		if err != nil {
			_ = d.DeleteProfile(profileID)
			t.Fatal(err)
		}
		if err := d.SetAgentLLMProfile(agent.Key, &profileID); err != nil {
			_ = d.DeleteAgent(agent.Key)
			_ = d.DeleteProfile(profileID)
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = d.DeleteAgent(agent.Key)
			_ = d.DeleteProfile(profileID)
		})

		blocker, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err := blocker.Exec(`SELECT id FROM agents WHERE id=$1`, agent.ID); err != nil {
			t.Fatal(err)
		}
		deleteDB, deletePID := openSingleConnectionTestDB(t, dsn)
		deleteDone := make(chan error, 1)
		go func() { deleteDone <- deleteDB.DeleteProfile(profileID) }()
		if err := waitForBackendBlock(d, deletePID, deleteDone); err != nil {
			blocker.Rollback()
			t.Fatal(err)
		}
		assertProfilesUnlocked(t, d, profileID)
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := waitForMutationResult(deleteDone); err != nil {
			t.Fatalf("delete agent profile: %v", err)
		}
		got, err := d.GetAgentByKey(agent.Key)
		if err != nil || got == nil || got.LLMProfileID != nil {
			t.Fatalf("agent binding was not cleared: agent=%+v err=%v", got, err)
		}
	})

	t.Run("conversation", func(t *testing.T) {
		d, err := Open(dsn)
		if err != nil {
			t.Fatalf("SQLite unavailable (%v) - skipping", err)
		}
		defer d.Close()

		suffix := time.Now().UnixNano()
		profileID, err := d.SaveProfile(&LLMProfile{
			Name: fmt.Sprintf("conversation-lock-profile-%d", suffix), Format: "openai", Model: "conversation-lock", APIKey: "test-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		conversation, err := d.CreateConversation("planner", "lock conversation", &profileID)
		if err != nil {
			_ = d.DeleteProfile(profileID)
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = d.DeleteConversation(conversation.ID)
			_ = d.DeleteProfile(profileID)
		})

		blocker, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err := blocker.Exec(`SELECT id FROM conversations WHERE id=$1`, conversation.ID); err != nil {
			t.Fatal(err)
		}
		deleteDB, deletePID := openSingleConnectionTestDB(t, dsn)
		deleteDone := make(chan error, 1)
		go func() { deleteDone <- deleteDB.DeleteProfile(profileID) }()
		if err := waitForBackendBlock(d, deletePID, deleteDone); err != nil {
			blocker.Rollback()
			t.Fatal(err)
		}
		assertProfilesUnlocked(t, d, profileID)
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := waitForMutationResult(deleteDone); err != nil {
			t.Fatalf("delete conversation profile: %v", err)
		}
		got, err := d.GetConversation(conversation.ID)
		if err != nil || got == nil || got.LLMProfileID != nil {
			t.Fatalf("conversation binding was not cleared: conversation=%+v err=%v", got, err)
		}
	})
}

func TestNonTaskProfileMutationsLockReferenceBeforeProfile(t *testing.T) {
	dsn := testDSN(t)

	t.Run("agent", func(t *testing.T) {
		d, err := Open(dsn)
		if err != nil {
			t.Fatalf("SQLite unavailable (%v) - skipping", err)
		}
		defer d.Close()

		suffix := time.Now().UnixNano()
		profileID, err := d.SaveProfile(&LLMProfile{
			Name: fmt.Sprintf("agent-write-lock-profile-%d", suffix), Format: "openai", Model: "agent-write-lock", APIKey: "test-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		agent, err := d.CreateAgent(fmt.Sprintf("write_lock_agent_%d", suffix), "write lock agent", "")
		if err != nil {
			_ = d.DeleteProfile(profileID)
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = d.DeleteAgent(agent.Key)
			_ = d.DeleteProfile(profileID)
		})

		blocker, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err := blocker.Exec(`SELECT id FROM agents WHERE id=$1`, agent.ID); err != nil {
			t.Fatal(err)
		}
		mutationDB, mutationPID := openSingleConnectionTestDB(t, dsn)
		mutationDone := make(chan error, 1)
		go func() { mutationDone <- mutationDB.SetAgentLLMProfile(agent.Key, &profileID) }()
		if err := waitForBackendBlock(d, mutationPID, mutationDone); err != nil {
			blocker.Rollback()
			t.Fatal(err)
		}
		assertProfilesUnlocked(t, d, profileID)
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := waitForMutationResult(mutationDone); err != nil {
			t.Fatalf("bind agent profile: %v", err)
		}
	})

	t.Run("conversation", func(t *testing.T) {
		d, err := Open(dsn)
		if err != nil {
			t.Fatalf("SQLite unavailable (%v) - skipping", err)
		}
		defer d.Close()

		suffix := time.Now().UnixNano()
		profileID, err := d.SaveProfile(&LLMProfile{
			Name: fmt.Sprintf("conversation-write-lock-profile-%d", suffix), Format: "openai", Model: "conversation-write-lock", APIKey: "test-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		conversation, err := d.CreateConversation("planner", "write lock conversation", nil)
		if err != nil {
			_ = d.DeleteProfile(profileID)
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = d.DeleteConversation(conversation.ID)
			_ = d.DeleteProfile(profileID)
		})

		blocker, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err := blocker.Exec(`SELECT id FROM conversations WHERE id=$1`, conversation.ID); err != nil {
			t.Fatal(err)
		}
		mutationDB, mutationPID := openSingleConnectionTestDB(t, dsn)
		mutationDone := make(chan error, 1)
		go func() { mutationDone <- mutationDB.UpdateConversationProfile(conversation.ID, &profileID) }()
		if err := waitForBackendBlock(d, mutationPID, mutationDone); err != nil {
			blocker.Rollback()
			t.Fatal(err)
		}
		assertProfilesUnlocked(t, d, profileID)
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := waitForMutationResult(mutationDone); err != nil {
			t.Fatalf("bind conversation profile: %v", err)
		}
	})
}

func TestCreateConversationAndDeleteProfileDoNotDeadlock(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("SQLite unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	profileID, err := d.SaveProfile(&LLMProfile{
		Name: fmt.Sprintf("conversation-create-race-profile-%d", suffix), Format: "openai", Model: "conversation-create-race", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	title := fmt.Sprintf("conversation create race %d", suffix)
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM conversations WHERE title=$1`, title)
		_ = d.DeleteProfile(profileID)
	})

	profileBlocker, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer profileBlocker.Rollback()
	if _, err := profileBlocker.Exec(`SELECT id FROM llm_profiles WHERE id=$1`, profileID); err != nil {
		t.Fatal(err)
	}

	createDB, createPID := openSingleConnectionTestDB(t, dsn)
	type createResult struct {
		conversation *Conversation
		err          error
	}
	createDone := make(chan createResult, 1)
	createStatus := make(chan error, 1)
	go func() {
		conversation, err := createDB.CreateConversation("planner", title, &profileID)
		createDone <- createResult{conversation: conversation, err: err}
		createStatus <- err
	}()
	if err := waitForBackendBlock(d, createPID, createStatus); err != nil {
		profileBlocker.Rollback()
		t.Fatal(err)
	}

	deleteDB, deletePID := openSingleConnectionTestDB(t, dsn)
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- deleteDB.DeleteProfile(profileID) }()
	if err := waitForBackendBlock(d, deletePID, deleteDone); err != nil {
		profileBlocker.Rollback()
		t.Fatal(err)
	}
	if err := profileBlocker.Rollback(); err != nil {
		t.Fatal(err)
	}

	var created createResult
	select {
	case created = <-createDone:
	case <-time.After(12 * time.Second):
		t.Fatal("timed out waiting for conversation creation")
	}
	if err := waitForMutationResult(deleteDone); err != nil {
		t.Fatalf("delete profile during conversation creation: %v", err)
	}
	var persisted int
	if err := d.QueryRow(`SELECT count(*) FROM conversations WHERE title=$1`, title).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if created.err == nil {
		if created.conversation == nil || persisted != 1 {
			t.Fatalf("successful creation was not committed atomically: conversation=%+v count=%d", created.conversation, persisted)
		}
	} else if persisted != 0 {
		t.Fatalf("failed creation left a partial conversation row: err=%v count=%d", created.err, persisted)
	}
}

func TestDeleteProfileRetriesReferenceCommittedAfterInitialScan(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("SQLite unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	profileID, err := d.SaveProfile(&LLMProfile{
		Name: fmt.Sprintf("late-reference-profile-%d", suffix), Format: "openai", Model: "late-reference", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask("late profile reference", "exercise delete retry", nil, 0, 0)
	if err != nil {
		_ = d.DeleteProfile(profileID)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_ = d.DeleteProfile(profileID)
	})

	// Keep the new reference uncommitted while deletion takes its initial
	// READ COMMITTED snapshot. The task is therefore absent from the first lock
	// set, while its FK KEY SHARE lock makes deletion wait at the profile row.
	referenceTx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer referenceTx.Rollback()
	if _, err := referenceTx.Exec(`UPDATE tasks
SET llm_profile_id=$2, active_llm_profile_id=$2
WHERE id=$1`, task.ID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := referenceTx.Exec(`INSERT INTO task_llm_profiles(task_id, profile_id, position)
VALUES ($1,$2,0)`, task.ID, profileID); err != nil {
		t.Fatal(err)
	}

	deleteDB, deletePID := openSingleConnectionTestDB(t, dsn)
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- deleteDB.DeleteProfile(profileID) }()
	if err := waitForBackendBlock(d, deletePID, deleteDone); err != nil {
		referenceTx.Rollback()
		t.Fatal(err)
	}
	if err := referenceTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := waitForMutationResult(deleteDone); err != nil {
		t.Fatalf("delete profile after late reference: %v", err)
	}

	got, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID != nil || len(got.LLMProfileIDs) != 0 || got.LLMChainRevision != 1 {
		t.Fatalf("late task reference was not handled by a locked retry: %+v", got)
	}
}

func openSingleConnectionTestDB(t *testing.T, dsn string) (*DB, int) {
	t.Helper()
	sqlDB, err := sql.Open("prtseek-sqlite", dsn+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(`PRAGMA busy_timeout=10000`); err != nil {
		t.Fatal(err)
	}
	pid := 0
	return &DB{sqlDB}, pid
}

func waitForBackendBlock(observer *DB, pid int, done <-chan error) error {
	select {
	case err := <-done:
		return fmt.Errorf("writer completed before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}
func assertProfilesUnlocked(t *testing.T, d *DB, profileIDs ...int64) {
	t.Helper()
	for _, id := range profileIDs {
		var found int64
		if e := d.QueryRow(`SELECT id FROM llm_profiles WHERE id=$1`, id).Scan(&found); e != nil {
			t.Fatalf("WAL reader blocked: %v", e)
		}
	}
}

func waitForMutationResult(done <-chan error) error {
	select {
	case err := <-done:
		return err
	case <-time.After(12 * time.Second):
		return fmt.Errorf("timed out waiting for task LLM mutation")
	}
}
