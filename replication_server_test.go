package lplex

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstanceManagerCreateAndGet(t *testing.T) {
	dir := t.TempDir()
	im, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	inst := im.GetOrCreate("boat-1")
	if inst.ID != "boat-1" {
		t.Fatalf("ID: got %q, want %q", inst.ID, "boat-1")
	}
	if inst.Cursor != 0 {
		t.Fatalf("new instance should have cursor=0, got %d", inst.Cursor)
	}

	// GetOrCreate returns same instance
	inst2 := im.GetOrCreate("boat-1")
	if inst2 != inst {
		t.Fatal("GetOrCreate should return same pointer for existing instance")
	}

	// Get works
	got := im.Get("boat-1")
	if got != inst {
		t.Fatal("Get should return the instance")
	}

	// Get unknown returns nil
	if im.Get("nope") != nil {
		t.Fatal("Get should return nil for unknown instance")
	}
}

func TestInstanceManagerPersistAndReload(t *testing.T) {
	dir := t.TempDir()

	// Create an instance, mutate state, shut down
	im1, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	inst := im1.GetOrCreate("boat-42")
	inst.mu.Lock()
	inst.Cursor = 5000
	inst.BoatHeadSeq = 8000
	inst.BoatJournalBytes = 1024 * 1024
	inst.LastSeen = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	inst.HoleTracker.Add(5001, 7000)
	inst.HoleTracker.Add(7500, 8000)
	inst.mu.Unlock()

	im1.Shutdown()

	// Verify state.json was written
	stateFile := filepath.Join(dir, "instances", "boat-42", "state.json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("state.json not written: %v", err)
	}

	var persisted instanceStatePersist
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("bad state.json: %v", err)
	}
	if persisted.Cursor != 5000 {
		t.Fatalf("persisted cursor: got %d, want 5000", persisted.Cursor)
	}
	if len(persisted.Holes) != 2 {
		t.Fatalf("persisted holes: got %d, want 2", len(persisted.Holes))
	}

	// Reload from disk
	im2, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im2.Shutdown()

	reloaded := im2.Get("boat-42")
	if reloaded == nil {
		t.Fatal("reloaded instance should exist")
	}
	if reloaded.Cursor != 5000 {
		t.Fatalf("reloaded cursor: got %d, want 5000", reloaded.Cursor)
	}
	if reloaded.BoatHeadSeq != 8000 {
		t.Fatalf("reloaded boat_head_seq: got %d, want 8000", reloaded.BoatHeadSeq)
	}
	if reloaded.HoleTracker.Len() != 2 {
		t.Fatalf("reloaded holes: got %d, want 2", reloaded.HoleTracker.Len())
	}
	holes := reloaded.HoleTracker.Holes()
	if holes[0].Start != 5001 || holes[0].End != 7000 {
		t.Fatalf("hole 0: got [%d, %d), want [5001, 7000)", holes[0].Start, holes[0].End)
	}
	if holes[1].Start != 7500 || holes[1].End != 8000 {
		t.Fatalf("hole 1: got [%d, %d), want [7500, 8000)", holes[1].Start, holes[1].End)
	}
}

func TestInstanceManagerLoadMissingStateFile(t *testing.T) {
	dir := t.TempDir()

	// Create instance directory without state.json
	instDir := filepath.Join(dir, "instances", "orphan")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	im, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	// Should load with defaults
	inst := im.Get("orphan")
	if inst == nil {
		t.Fatal("orphaned instance directory should still be loaded")
	}
	if inst.Cursor != 0 {
		t.Fatalf("orphan cursor: got %d, want 0", inst.Cursor)
	}
}

func TestInstanceManagerList(t *testing.T) {
	dir := t.TempDir()
	im, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	im.GetOrCreate("boat-a")
	im.GetOrCreate("boat-b")

	inst := im.GetOrCreate("boat-c")
	inst.mu.Lock()
	inst.Cursor = 100
	inst.BoatHeadSeq = 200
	inst.Connected = true
	inst.mu.Unlock()

	summaries := im.List()
	if len(summaries) != 3 {
		t.Fatalf("List: got %d instances, want 3", len(summaries))
	}

	// Find boat-c in the list
	var found bool
	for _, s := range summaries {
		if s.ID == "boat-c" {
			found = true
			if !s.Connected {
				t.Error("boat-c should be connected")
			}
			if s.Cursor != 100 {
				t.Errorf("boat-c cursor: got %d, want 100", s.Cursor)
			}
			if s.LagSeqs != 100 {
				t.Errorf("boat-c lag: got %d, want 100", s.LagSeqs)
			}
		}
	}
	if !found {
		t.Fatal("boat-c not found in List()")
	}
}

func TestInstanceStateStatus(t *testing.T) {
	dir := t.TempDir()
	im, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	inst := im.GetOrCreate("test")
	inst.mu.Lock()
	inst.Cursor = 500
	inst.BoatHeadSeq = 600
	inst.BoatJournalBytes = 42000
	inst.Connected = true
	inst.HoleTracker.Add(501, 550)
	inst.mu.Unlock()

	status := inst.Status()
	if status.ID != "test" {
		t.Fatalf("ID: got %q, want %q", status.ID, "test")
	}
	if !status.Connected {
		t.Fatal("should be connected")
	}
	if status.Cursor != 500 {
		t.Fatalf("cursor: got %d, want 500", status.Cursor)
	}
	if status.LagSeqs != 100 {
		t.Fatalf("lag: got %d, want 100", status.LagSeqs)
	}
	if len(status.Holes) != 1 {
		t.Fatalf("holes: got %d, want 1", len(status.Holes))
	}
}

func TestInstanceManagerEnsureBrokerIdempotent(t *testing.T) {
	dir := t.TempDir()
	im, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	inst := im.GetOrCreate("test")
	inst.mu.Lock()
	inst.ensureBroker()
	broker1 := inst.broker
	inst.ensureBroker()
	broker2 := inst.broker
	inst.mu.Unlock()

	if broker1 != broker2 {
		t.Fatal("ensureBroker should return same broker on second call")
	}
	if broker1 == nil {
		t.Fatal("broker should not be nil after ensureBroker")
	}
}

func TestInstanceManagerStopBrokerCleanup(t *testing.T) {
	dir := t.TempDir()
	im, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	inst := im.GetOrCreate("test")

	inst.mu.Lock()
	inst.ensureBroker()
	if inst.broker == nil {
		t.Fatal("broker should exist after ensureBroker")
	}
	inst.stopBroker()
	if inst.broker != nil {
		t.Fatal("broker should be nil after stopBroker")
	}
	if inst.journalCh != nil {
		t.Fatal("journalCh should be nil after stopBroker")
	}
	if inst.cancelFunc != nil {
		t.Fatal("cancelFunc should be nil after stopBroker")
	}

	// stopBroker on nil broker should be safe
	inst.stopBroker()
	inst.mu.Unlock()
}

func TestInstanceManagerShutdownPersistsAll(t *testing.T) {
	dir := t.TempDir()
	im, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	a := im.GetOrCreate("boat-a")
	a.mu.Lock()
	a.Cursor = 100
	a.mu.Unlock()

	b := im.GetOrCreate("boat-b")
	b.mu.Lock()
	b.Cursor = 200
	b.mu.Unlock()

	im.Shutdown()

	// Both should have state files
	for _, id := range []string{"boat-a", "boat-b"} {
		path := filepath.Join(dir, "instances", id, "state.json")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s state.json missing: %v", id, err)
		}
	}

	// Reload and verify
	im2, err := NewInstanceManager(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer im2.Shutdown()

	if im2.Get("boat-a").Cursor != 100 {
		t.Fatalf("boat-a cursor after reload: got %d, want 100", im2.Get("boat-a").Cursor)
	}
	if im2.Get("boat-b").Cursor != 200 {
		t.Fatalf("boat-b cursor after reload: got %d, want 200", im2.Get("boat-b").Cursor)
	}
}
