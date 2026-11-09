package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func personalTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InitPersonalBackgroundSchema(); err != nil {
		d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func personalTestConversation(t *testing.T, d *DB) *Conversation {
	t.Helper()
	c, err := d.CreateConversation("auto", "personal handoff test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rows, err := d.Query(`SELECT task_id FROM conversation_task_runs WHERE conversation_id=$1`, c.ID)
		if err != nil {
			t.Error(err)
			return
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Error(err)
				continue
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := d.DeleteTask(id); err != nil {
				t.Error(err)
			}
		}
		if err := d.DeleteConversation(c.ID); err != nil {
			t.Error(err)
		}
	})
	return c
}

func personalTestHandoff(c *Conversation, key string) *PersonalHandoffInput {
	return &PersonalHandoffInput{ConversationID: c.ID, IdempotencyKey: key, SourceCursor: 17, Checkpoint: "Earlier evidence stays at the checkpoint path; inspect before continuing.", ConfigSnapshot: json.RawMessage(`{"language":"zh","reasoning_effort":"medium"}`)}
}

func TestPersonalHandoffAtomicAndIdempotent(t *testing.T) {
	d := personalTestDB(t)
	c := personalTestConversation(t, d)
	counts := func() (int, int) {
		t.Helper()
		var tasks, explorations int
		if err := d.QueryRow(`SELECT count(*) FROM tasks`).Scan(&tasks); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT count(*) FROM explorations`).Scan(&explorations); err != nil {
			t.Fatal(err)
		}
		return tasks, explorations
	}
	beforeTasks, beforeExplorations := counts()
	h := personalTestHandoff(c, "first")
	// This validation happens after the exploration insert inside the task tx.
	if _, err := d.CreateTaskWithOptions("bad", "goal", TaskCreateOptions{WorkerCount: MaxTaskWorkerCount + 1, PersonalHandoff: h}); err == nil {
		t.Fatal("invalid task unexpectedly created")
	}
	a, b := counts()
	if a != beforeTasks || b != beforeExplorations {
		t.Fatalf("failed create leaked task/exploration: %d/%d -> %d/%d", beforeTasks, beforeExplorations, a, b)
	}
	if run, err := d.PersonalBackgroundRun(c.ID, ""); err != nil || run != nil {
		t.Fatalf("failed create left handoff: %+v %v", run, err)
	}
	task, err := d.CreateTaskWithOptions("valid", "goal", TaskCreateOptions{WorkerCount: 1, PersonalHandoff: h})
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.PersonalBackgroundRun(c.ID, "first")
	if err != nil || run == nil {
		t.Fatalf("handoff absent: %v", err)
	}
	if run.TaskID != task.ID || run.SourceCursor != 17 || !run.Active || run.Checkpoint != h.Checkpoint {
		t.Fatalf("handoff data lost: %+v", run)
	}
	hints, err := d.Exploration(task.ExplorationID).ListByKind("hint", 10)
	if err != nil || len(hints) != 1 {
		t.Fatalf("checkpoint not delivered to exploration: %d %v", len(hints), err)
	}
	var hint map[string]any
	if err := json.Unmarshal(hints[0].Payload, &hint); err != nil || hint["text"] != h.Checkpoint {
		t.Fatalf("checkpoint hint mismatch: %v %v", hint, err)
	}
	if _, err = d.CreateTaskWithOptions("duplicate", "goal", TaskCreateOptions{PersonalHandoff: h}); !errors.Is(err, ErrPersonalHandoffExists) {
		t.Fatalf("duplicate error=%v", err)
	}
	if _, err = d.CreateTaskWithOptions("another", "goal", TaskCreateOptions{PersonalHandoff: personalTestHandoff(c, "second")}); !errors.Is(err, ErrPersonalBackgroundActive) {
		t.Fatalf("active conflict error=%v", err)
	}
	a, b = counts()
	if a != beforeTasks+1 || b != beforeExplorations+1 {
		t.Fatal("rejected requests created extra tasks/explorations")
	}
	// Preserve original conversation/file references while a run refers to it.
	if err := d.DeleteConversation(c.ID); err == nil {
		t.Fatal("referenced conversation was deleted")
	}
	if _, err := d.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateTaskWithOptions("next", "goal", TaskCreateOptions{PersonalHandoff: personalTestHandoff(c, "second")})
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := d.PersonalBackgroundRun(c.ID, "")
	old, _ := d.PersonalBackgroundRun(c.ID, "first")
	if latest == nil || latest.TaskID != second.ID || old == nil || old.Active {
		t.Fatalf("history/active state mismatch: latest=%+v old=%+v", latest, old)
	}
	if _, err = d.CreateTaskWithOptions("old retry", "goal", TaskCreateOptions{PersonalHandoff: h}); !errors.Is(err, ErrPersonalHandoffExists) {
		t.Fatalf("completed retry lost idempotency: %v", err)
	}
}

func TestPersonalHandoffConcurrentRequests(t *testing.T) {
	d := personalTestDB(t)
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprint("same_key=", sameKey), func(t *testing.T) {
			c := personalTestConversation(t, d)
			var wg sync.WaitGroup
			results := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					key := "shared"
					if !sameKey {
						key = fmt.Sprint("request-", i)
					}
					_, err := d.CreateTaskWithOptions("parallel", "goal", TaskCreateOptions{PersonalHandoff: personalTestHandoff(c, key)})
					results <- err
				}(i)
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
					continue
				}
				want := ErrPersonalBackgroundActive
				if sameKey {
					want = ErrPersonalHandoffExists
				}
				if !errors.Is(err, want) {
					t.Errorf("unexpected conflict: %v", err)
				}
			}
			if success != 1 {
				t.Fatalf("created %d tasks; expected exactly one", success)
			}
			var total, active int
			if err := d.QueryRow(`SELECT count(*),count(*) FILTER(WHERE active) FROM conversation_task_runs WHERE conversation_id=$1`, c.ID).Scan(&total, &active); err != nil {
				t.Fatal(err)
			}
			if total != 1 || active != 1 {
				t.Fatalf("run count=%d active=%d", total, active)
			}
		})
	}
}

func TestPersonalBackgroundRestartRecovery(t *testing.T) {
	d := personalTestDB(t)
	type fixture struct {
		task *Task
		conv *Conversation
	}
	makeRun := func(state string, paused bool) fixture {
		t.Helper()
		c := personalTestConversation(t, d)
		task, err := d.CreateTaskWithOptions("recovery", "goal", TaskCreateOptions{PersonalHandoff: personalTestHandoff(c, "run")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`UPDATE tasks SET status=$2,paused=$3,queued=true,queued_at=(strftime('%Y-%m-%d %H:%M:%f','now')) WHERE id=$1`, task.ID, state, paused); err != nil {
			t.Fatal(err)
		}
		return fixture{task, c}
	}
	interrupted := makeRun("running", false)
	paused := makeRun("running", true)
	done := makeRun("done", false)
	if err := d.RecoverPersonalBackgroundTasks(); err != nil {
		t.Fatal(err)
	}
	run, _ := d.PersonalBackgroundRun(interrupted.conv.ID, "")
	task, _ := d.GetTask(interrupted.task.ID)
	if run == nil || run.State != "needs_review" || !run.Active || task == nil || !task.Paused || task.Queued {
		t.Fatalf("interrupted operation could replay: run=%+v task=%+v", run, task)
	}
	run, _ = d.PersonalBackgroundRun(paused.conv.ID, "")
	task, _ = d.GetTask(paused.task.ID)
	if run == nil || run.State != "active" || !run.Active || task == nil || !task.Paused {
		t.Fatalf("existing pause lost: run=%+v task=%+v", run, task)
	}
	run, _ = d.PersonalBackgroundRun(done.conv.ID, "")
	if run == nil || run.Active {
		t.Fatalf("terminal run stayed active: %+v", run)
	}
	if err := d.RecoverPersonalBackgroundTasks(); err != nil {
		t.Fatal(err)
	}
	run, _ = d.PersonalBackgroundRun(interrupted.conv.ID, "")
	if run.State != "needs_review" {
		t.Fatal("second recovery silently released review state")
	}
}
