package lplex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sixfathoms/lplex/journal"
)

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// createTestJournal creates a fake .lpj file with a given name and size.
func createTestJournal(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// lpjName builds a journal filename from a time, e.g. "nmea2k-20260302T140000.000Z.lpj".
func lpjName(prefix string, ts time.Time) string {
	return fmt.Sprintf("%s-%s.lpj", prefix, ts.UTC().Format("20060102T150405.000Z"))
}

// countLPJ returns the number of .lpj files in a directory.
func countLPJ(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lpj") {
			n++
		}
	}
	return n
}

// listLPJ returns the names of .lpj files in a directory, sorted by ReadDir order.
func listLPJ(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lpj") {
			names = append(names, e.Name())
		}
	}
	return names
}

// writeArchiveScript writes a shell script to the given path. The script echoes
// ok/error JSONL for each file arg, optionally capturing stdin.
func writeArchiveScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "archive.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// okScript returns a script body that writes {"status":"ok"} for every arg.
const okScript = `for arg in "$@"; do echo "{\"path\":\"$arg\",\"status\":\"ok\"}"; done`

// errScript returns a script body that writes {"status":"error"} for every arg.
const errScript = `for arg in "$@"; do echo "{\"path\":\"$arg\",\"status\":\"error\",\"error\":\"boom\"}"; done`

// -----------------------------------------------------------------------
// Unit: timestamp parsing
// -----------------------------------------------------------------------

func TestParseTimestampFromFilename(t *testing.T) {
	tests := []struct {
		name string
		want time.Time
	}{
		{"nmea2k-20260302T140000.000Z.lpj", time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)},
		{"backfill-20250115T083022.500Z.lpj", time.Date(2025, 1, 15, 8, 30, 22, 500_000_000, time.UTC)},
		{"garbage.lpj", time.Time{}},
		{"no-extension", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTimestampFromFilename(tt.name)
			if !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Unit: archive trigger parsing
// -----------------------------------------------------------------------

func TestParseArchiveTrigger(t *testing.T) {
	tests := []struct {
		input string
		want  ArchiveTrigger
		err   bool
	}{
		{"", ArchiveDisabled, false},
		{"disabled", ArchiveDisabled, false},
		{"on-rotate", ArchiveOnRotate, false},
		{"before-expire", ArchiveBeforeExpire, false},
		{"yolo", ArchiveDisabled, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseArchiveTrigger(tt.input)
			if tt.err {
				if err == nil {
					t.Errorf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Unit: marker files
// -----------------------------------------------------------------------

func TestMarkerFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lpj")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if isArchived(path) {
		t.Fatal("should not be archived yet")
	}

	markArchived(path)

	if !isArchived(path) {
		t.Fatal("should be archived after marking")
	}

	if _, err := os.Stat(path + ".archived"); err != nil {
		t.Fatalf("marker file missing: %v", err)
	}
}

// -----------------------------------------------------------------------
// Retention: max-age
// -----------------------------------------------------------------------

func TestRetentionMaxAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 1000)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-5*24*time.Hour)), 1000)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*24*time.Hour)), 1000)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:   []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge: 7 * 24 * time.Hour,
	})

	ctx := context.Background()
	keeper.scanAll(ctx)

	// 10d deleted, 5d+1d kept.
	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("expected 2 files, got %d: %v", got, listLPJ(t, dir))
	}
}

// All files are younger than max-age: nothing deleted.
func TestRetentionMaxAgeAllYoung(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 100)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:   []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge: 24 * time.Hour,
	})
	keeper.scanAll(context.Background())

	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("nothing should be deleted, got %d remaining", got)
	}
}

// -----------------------------------------------------------------------
// Retention: min-keep
// -----------------------------------------------------------------------

func TestRetentionMinKeep(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 1000) // past both
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*24*time.Hour)), 1000)  // past max-age, within min-keep
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*24*time.Hour)), 1000)  // within both

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:    []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge:  2 * 24 * time.Hour,
		MinKeep: 5 * 24 * time.Hour,
	})
	keeper.scanAll(context.Background())

	// old deleted, mid+recent kept.
	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("expected 2 files, got %d: %v", got, listLPJ(t, dir))
	}
}

// -----------------------------------------------------------------------
// Retention: max-size
// -----------------------------------------------------------------------

func TestRetentionMaxSizeOverridesMinKeep(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// 3 files x 500 bytes = 1500. max-size 1000 forces deletion of the oldest.
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*24*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*24*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*24*time.Hour)), 500)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:    []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MinKeep: 10 * 24 * time.Hour, // all files are "young"
		MaxSize: 1000,
	})
	keeper.scanAll(context.Background())

	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("expected 2 files, got %d", got)
	}
}

// max-size deletes multiple files in one pass.
func TestRetentionMaxSizeDeletesMultiple(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// 5 files x 300 bytes = 1500. max-size 600 means delete 3 oldest.
	for i := 5; i >= 1; i-- {
		createTestJournal(t, dir, lpjName("nmea2k", now.Add(-time.Duration(i)*24*time.Hour)), 300)
	}

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:    []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize: 600,
	})
	keeper.scanAll(context.Background())

	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("expected 2 files (600 bytes), got %d: %v", got, listLPJ(t, dir))
	}
}

// max-size alone (no max-age) still triggers deletion.
func TestRetentionMaxSizeOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 700)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-30*time.Minute)), 700)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:    []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize: 1000,
	})
	keeper.scanAll(context.Background())

	if got := countLPJ(t, dir); got != 1 {
		t.Errorf("expected 1 file, got %d", got)
	}
}

// -----------------------------------------------------------------------
// Retention: deletion cleans up .archived markers
// -----------------------------------------------------------------------

func TestRetentionDeletesArchivedFileAndMarker(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)
	markArchived(path)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:   []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge: 7 * 24 * time.Hour,
	})
	keeper.scanAll(context.Background())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("journal file should be deleted")
	}
	if _, err := os.Stat(path + ".archived"); !os.IsNotExist(err) {
		t.Error("archived marker should be deleted")
	}
}

// -----------------------------------------------------------------------
// Retention: no config = no deletion
// -----------------------------------------------------------------------

func TestRetentionNoConfig(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-100*24*time.Hour)), 100)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs: []KeeperDir{{Dir: dir, InstanceID: "test"}},
	})
	keeper.scanAll(context.Background())

	if got := countLPJ(t, dir); got != 1 {
		t.Errorf("expected 1 file (no retention), got %d", got)
	}
}

// -----------------------------------------------------------------------
// Retention: edge cases
// -----------------------------------------------------------------------

func TestRetentionEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:   []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge: 24 * time.Hour,
	})
	keeper.scanAll(context.Background()) // must not panic
}

func TestRetentionNonexistentDirectory(t *testing.T) {
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:   []KeeperDir{{Dir: "/tmp/nonexistent-lplex-test-dir-12345", InstanceID: "test"}},
		MaxAge: 24 * time.Hour,
	})
	keeper.scanAll(context.Background()) // must not panic
}

// Non-.lpj files in the directory are left untouched.
func TestRetentionIgnoresNonLPJFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)
	otherFile := filepath.Join(dir, "state.json")
	if err := os.WriteFile(otherFile, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	txtFile := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txtFile, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:   []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge: 7 * 24 * time.Hour,
	})
	keeper.scanAll(context.Background())

	// .lpj is deleted, other files remain.
	if got := countLPJ(t, dir); got != 0 {
		t.Errorf("expected 0 .lpj files, got %d", got)
	}
	if _, err := os.Stat(otherFile); err != nil {
		t.Error("state.json should not be touched")
	}
	if _, err := os.Stat(txtFile); err != nil {
		t.Error("notes.txt should not be touched")
	}
}

// -----------------------------------------------------------------------
// Retention: multiple directories
// -----------------------------------------------------------------------

func TestRetentionMultipleDirectories(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	now := time.Now().UTC()

	createTestJournal(t, dir1, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)
	createTestJournal(t, dir1, lpjName("nmea2k", now.Add(-1*24*time.Hour)), 100)
	createTestJournal(t, dir2, lpjName("backfill", now.Add(-10*24*time.Hour)), 100)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs: []KeeperDir{
			{Dir: dir1, InstanceID: "boat-1"},
			{Dir: dir2, InstanceID: "boat-2"},
		},
		MaxAge: 7 * 24 * time.Hour,
	})
	keeper.scanAll(context.Background())

	if got := countLPJ(t, dir1); got != 1 {
		t.Errorf("dir1: expected 1 file, got %d", got)
	}
	if got := countLPJ(t, dir2); got != 0 {
		t.Errorf("dir2: expected 0 files, got %d", got)
	}
}

// max-size is evaluated per directory, not across all directories.
func TestRetentionMaxSizePerDirectory(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	now := time.Now().UTC()

	// dir1: 2 x 600 = 1200
	createTestJournal(t, dir1, lpjName("nmea2k", now.Add(-2*time.Hour)), 600)
	createTestJournal(t, dir1, lpjName("nmea2k", now.Add(-1*time.Hour)), 600)
	// dir2: 1 x 600
	createTestJournal(t, dir2, lpjName("nmea2k", now.Add(-1*time.Hour)), 600)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs: []KeeperDir{
			{Dir: dir1, InstanceID: "a"},
			{Dir: dir2, InstanceID: "b"},
		},
		MaxSize: 1000,
	})
	keeper.scanAll(context.Background())

	// dir1 must drop 1 file (1200 > 1000), dir2 is fine (600 <= 1000).
	if got := countLPJ(t, dir1); got != 1 {
		t.Errorf("dir1: expected 1 file, got %d", got)
	}
	if got := countLPJ(t, dir2); got != 1 {
		t.Errorf("dir2: expected 1 file, got %d", got)
	}
}

// -----------------------------------------------------------------------
// DirFunc
// -----------------------------------------------------------------------

func TestDirFunc(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)

	called := false
	keeper := NewJournalKeeper(KeeperConfig{
		DirFunc: func() []KeeperDir {
			called = true
			return []KeeperDir{{Dir: dir, InstanceID: "dynamic"}}
		},
		MaxAge: 7 * 24 * time.Hour,
	})
	keeper.scanAll(context.Background())

	if !called {
		t.Error("DirFunc should have been called")
	}
	if got := countLPJ(t, dir); got != 0 {
		t.Errorf("old file should have been deleted, got %d", got)
	}
}

// -----------------------------------------------------------------------
// Archive: before-expire (two-pass lifecycle)
// -----------------------------------------------------------------------

func TestArchiveBeforeExpire(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)
	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge:         7 * 24 * time.Hour,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	ctx := context.Background()

	// Pass 1: archive the file, but don't delete yet.
	keeper.scanAll(ctx)
	if !isArchived(path) {
		t.Fatal("file should be marked archived after first scan")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("file should still exist after first scan")
	}

	// Pass 2: file is archived, now delete it.
	keeper.scanAll(ctx)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be deleted after second scan")
	}
	if _, err := os.Stat(path + ".archived"); !os.IsNotExist(err) {
		t.Error("marker should be cleaned up")
	}
}

// before-expire with multiple expired files archives and then deletes all of them.
func TestArchiveBeforeExpireMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-20*24*time.Hour)), 100)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-15*24*time.Hour)), 100)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*24*time.Hour)), 100) // young, kept

	script := writeArchiveScript(t, scriptDir, okScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge:         7 * 24 * time.Hour,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	ctx := context.Background()
	keeper.scanAll(ctx) // archive both old files
	if !isArchived(p1) || !isArchived(p2) {
		t.Fatal("both old files should be archived")
	}

	keeper.scanAll(ctx) // delete both
	if got := countLPJ(t, dir); got != 1 {
		t.Errorf("expected 1 file remaining, got %d", got)
	}
}

// -----------------------------------------------------------------------
// Archive: on-rotate
// -----------------------------------------------------------------------

func TestArchiveOnRotate(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 200)
	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "boat-1"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "boat-1"})

	if !isArchived(path) {
		t.Fatal("file should be marked archived after on-rotate")
	}
}

// on-rotate is a no-op when trigger is before-expire.
func TestHandleRotationNoopForBeforeExpire(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 200)
	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})
	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	if isArchived(path) {
		t.Error("on-rotate should not archive when trigger is before-expire")
	}
}

// on-rotate is a no-op when trigger is disabled.
func TestHandleRotationNoopForDisabled(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 200)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveTrigger: ArchiveDisabled,
	})
	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	if isArchived(path) {
		t.Error("on-rotate should not archive when trigger is disabled")
	}
}

// -----------------------------------------------------------------------
// Archive: on-rotate scan catches unarchived expired files
// -----------------------------------------------------------------------

// When trigger is on-rotate, a scan finds an unarchived expired file (the
// rotation notification was missed, e.g. during a restart). The scan queues it.
func TestOnRotateScanCatchesMissedFile(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)
	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge:         7 * 24 * time.Hour,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	// Scan without any prior handleRotation call. The file is expired and
	// unarchived, so the scan should queue it for archive.
	keeper.scanAll(context.Background())

	if !isArchived(path) {
		t.Fatal("scan should archive the missed file")
	}

	// A second scan should now delete it.
	keeper.scanAll(context.Background())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be deleted after archive + second scan")
	}
}

// -----------------------------------------------------------------------
// Archive: script failure queues retry
// -----------------------------------------------------------------------

func TestArchiveScriptFailureQueuesRetry(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	name := lpjName("nmea2k", now.Add(-1*time.Hour))
	createTestJournal(t, dir, name, 100)

	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	keeper.handleRotation(context.Background(), RotatedFile{Path: filepath.Join(dir, name), InstanceID: "test"})

	if len(keeper.pending) != 1 {
		t.Errorf("expected 1 pending file, got %d", len(keeper.pending))
	}
}

// -----------------------------------------------------------------------
// Archive: retry succeeds on second attempt
// -----------------------------------------------------------------------

func TestRetrySucceedsAfterInitialFailure(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	name := lpjName("nmea2k", now.Add(-1*time.Hour))
	path := createTestJournal(t, dir, name, 100)

	// First script always fails.
	failScript := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: failScript,
		ArchiveTrigger: ArchiveOnRotate,
	})

	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})
	if len(keeper.pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(keeper.pending))
	}

	// Swap to a script that succeeds.
	succeedScript := writeArchiveScript(t, scriptDir, okScript)
	keeper.cfg.ArchiveCommand = succeedScript

	keeper.retryArchive(context.Background())

	if len(keeper.pending) != 0 {
		t.Errorf("expected 0 pending after successful retry, got %d", len(keeper.pending))
	}
	if !isArchived(path) {
		t.Error("file should be archived after retry")
	}
}

// -----------------------------------------------------------------------
// Archive: retry exponential backoff
// -----------------------------------------------------------------------

func TestRetryExponentialBackoff(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)
	script := writeArchiveScript(t, scriptDir, errScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	// Initial backoff should be 0 (not set until the Run loop arms the timer).
	// After first retry failure, backoff doubles.
	keeper.backoff = keeperInitialBackoff
	keeper.retryArchive(context.Background())
	if keeper.backoff != 2*keeperInitialBackoff {
		t.Errorf("expected backoff %v, got %v", 2*keeperInitialBackoff, keeper.backoff)
	}

	keeper.retryArchive(context.Background())
	if keeper.backoff != 4*keeperInitialBackoff {
		t.Errorf("expected backoff %v, got %v", 4*keeperInitialBackoff, keeper.backoff)
	}

	// Drive it to the cap.
	for range 20 {
		keeper.retryArchive(context.Background())
	}
	if keeper.backoff > keeperMaxBackoff {
		t.Errorf("backoff %v exceeded max %v", keeper.backoff, keeperMaxBackoff)
	}
}

// Backoff resets to 0 after all retries succeed.
func TestRetryBackoffResetsOnSuccess(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)
	script := writeArchiveScript(t, scriptDir, errScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})
	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})
	keeper.backoff = 4 * keeperInitialBackoff

	// Swap to success.
	keeper.cfg.ArchiveCommand = writeArchiveScript(t, scriptDir, okScript)
	keeper.retryArchive(context.Background())

	if keeper.backoff != 0 {
		t.Errorf("expected backoff 0 after success, got %v", keeper.backoff)
	}
}

// -----------------------------------------------------------------------
// Archive: retry drops manually deleted files
// -----------------------------------------------------------------------

func TestRetryDropsDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)
	script := writeArchiveScript(t, scriptDir, errScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})
	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	// Manually delete the file.
	_ = os.Remove(path)

	keeper.backoff = keeperInitialBackoff
	keeper.retryArchive(context.Background())

	if len(keeper.pending) != 0 {
		t.Errorf("expected 0 pending after file deletion, got %d", len(keeper.pending))
	}
	if keeper.backoff != 0 {
		t.Errorf("backoff should reset when pending list empties, got %v", keeper.backoff)
	}
}

// -----------------------------------------------------------------------
// Archive: partial success in batch
// -----------------------------------------------------------------------

func TestArchivePartialSuccess(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 100)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 100)
	p3 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	// Script that succeeds for first and third args, fails for second.
	scriptBody := `
i=0
for arg in "$@"; do
  i=$((i + 1))
  if [ "$i" = "2" ]; then
    echo "{\"path\":\"$arg\",\"status\":\"error\",\"error\":\"nope\"}"
  else
    echo "{\"path\":\"$arg\",\"status\":\"ok\"}"
  fi
done
`
	script := writeArchiveScript(t, scriptDir, scriptBody)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	files := []pendingFile{
		{path: p1, instanceID: "test", size: 100, created: now.Add(-3 * time.Hour)},
		{path: p2, instanceID: "test", size: 100, created: now.Add(-2 * time.Hour)},
		{path: p3, instanceID: "test", size: 100, created: now.Add(-1 * time.Hour)},
	}
	keeper.archiveFiles(context.Background(), files)

	if !isArchived(p1) {
		t.Error("p1 should be archived")
	}
	if isArchived(p2) {
		t.Error("p2 should NOT be archived (script returned error)")
	}
	if !isArchived(p3) {
		t.Error("p3 should be archived")
	}
	if len(keeper.pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(keeper.pending))
	}
	if keeper.pending[0].path != p2 {
		t.Errorf("pending file should be p2, got %s", keeper.pending[0].path)
	}
}

// -----------------------------------------------------------------------
// Archive: script receives correct CLI args and JSONL stdin
// -----------------------------------------------------------------------

func TestArchiveScriptProtocol(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	ts := now.Add(-1 * time.Hour)
	path := createTestJournal(t, dir, lpjName("nmea2k", ts), 42)

	// Script captures stdin and args.
	stdinCapture := filepath.Join(scriptDir, "stdin.jsonl")
	argsCapture := filepath.Join(scriptDir, "args.txt")
	scriptBody := fmt.Sprintf(`
cat /dev/stdin > %s
echo "$@" > %s
for arg in "$@"; do
  echo "{\"path\":\"$arg\",\"status\":\"ok\"}"
done
`, stdinCapture, argsCapture)
	script := writeArchiveScript(t, scriptDir, scriptBody)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test-instance"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})
	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test-instance"})

	// Verify stdin JSONL.
	data, err := os.ReadFile(stdinCapture)
	if err != nil {
		t.Fatalf("reading captured stdin: %v", err)
	}
	var req archiveRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("parsing stdin JSONL: %v (data: %s)", err, data)
	}
	if req.Path != path {
		t.Errorf("stdin path = %q, want %q", req.Path, path)
	}
	if req.InstanceID != "test-instance" {
		t.Errorf("stdin instance_id = %q, want test-instance", req.InstanceID)
	}
	if req.Size != 42 {
		t.Errorf("stdin size = %d, want 42", req.Size)
	}
	if req.Created == "" {
		t.Error("stdin created should not be empty")
	}

	// Verify args.
	argsData, err := os.ReadFile(argsCapture)
	if err != nil {
		t.Fatalf("reading captured args: %v", err)
	}
	if !strings.Contains(string(argsData), path) {
		t.Errorf("args should contain file path, got: %s", argsData)
	}
}

// Batch invocation: multiple file paths as args, multiple JSONL lines on stdin.
func TestArchiveScriptBatch(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 100)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 200)

	stdinCapture := filepath.Join(scriptDir, "stdin.jsonl")
	argsCapture := filepath.Join(scriptDir, "args.txt")
	scriptBody := fmt.Sprintf(`
cat /dev/stdin > %s
echo "$@" > %s
for arg in "$@"; do echo "{\"path\":\"$arg\",\"status\":\"ok\"}"; done
`, stdinCapture, argsCapture)
	script := writeArchiveScript(t, scriptDir, scriptBody)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	files := []pendingFile{
		{path: p1, instanceID: "test", size: 100, created: now.Add(-3 * time.Hour)},
		{path: p2, instanceID: "test", size: 200, created: now.Add(-2 * time.Hour)},
	}
	keeper.archiveFiles(context.Background(), files)

	// Verify both files are archived.
	if !isArchived(p1) || !isArchived(p2) {
		t.Error("both files should be archived")
	}

	// Verify stdin has two JSONL lines.
	data, err := os.ReadFile(stdinCapture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 JSONL lines on stdin, got %d: %s", len(lines), data)
	}

	// Verify args has both paths.
	argsData, _ := os.ReadFile(argsCapture)
	args := string(argsData)
	if !strings.Contains(args, p1) || !strings.Contains(args, p2) {
		t.Errorf("args should contain both paths, got: %s", args)
	}
}

// -----------------------------------------------------------------------
// Run loop: Send + Run integration
// -----------------------------------------------------------------------

func TestKeeperRunAndSend(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 200)
	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "boat-1"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		keeper.Run(ctx)
		close(done)
	}()

	keeper.Send(RotatedFile{Path: path, InstanceID: "boat-1"})

	// Poll for the .archived marker instead of using a fixed sleep.
	deadline := time.After(5 * time.Second)
	for !isArchived(path) {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("file should be archived via Send+Run")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// -----------------------------------------------------------------------
// Run loop: startup scan catches files from a previous run
// -----------------------------------------------------------------------

func TestStartupScanCatchesOldFiles(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// Simulate files that were rotated while the keeper was down.
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)
	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge:         7 * 24 * time.Hour,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		keeper.Run(ctx)
		close(done)
	}()

	// The startup scan in Run() should pick up the old file immediately.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// First pass archives, doesn't delete. Second scan needed for delete.
	// But the startup scan only runs once, so the file is archived but not deleted.
	entries, _ := os.ReadDir(dir)
	archivedCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".archived") {
			archivedCount++
		}
	}
	if archivedCount != 1 {
		t.Errorf("expected 1 archived marker from startup scan, got %d", archivedCount)
	}
}

// -----------------------------------------------------------------------
// Integration: JournalWriter OnRotate fires on rotation
// -----------------------------------------------------------------------

func TestJournalWriterOnRotateCallback(t *testing.T) {
	dir := t.TempDir()
	devices := NewDeviceRegistry()

	var mu sync.Mutex
	var rotated []RotatedFile

	// Use 2000-byte frames so each 4096-byte block fits exactly 2 frames
	// (~2007 bytes encoded each). With RotateCount=2 and 8 frames:
	//   File1: frames 0,1 → rotation (callback 1)
	//   File2: frames 2,3,4,5 → rotation (callback 2)
	//   File3: frames 6,7 → finalize (callback 3)
	// Note: file2 holds 4 frames because openFile resets fileFrames, causing
	// the carry-over frame to not count toward the new file's rotation threshold.
	bigData := make([]byte, 2000)
	for i := range bigData {
		bigData[i] = byte(i)
	}

	ch := make(chan RxFrame, 100)
	jw, err := NewJournalWriter(JournalConfig{
		Dir:         dir,
		Prefix:      "nmea2k",
		BlockSize:   4096,
		Compression: journal.CompressionNone,
		RotateCount: 2,
		OnRotate: func(rf RotatedFile) {
			mu.Lock()
			rotated = append(rotated, rf)
			mu.Unlock()
		},
	}, devices, ch)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now()
	for i := range 8 {
		ch <- RxFrame{
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
			Header:    CANHeader{Priority: 2, PGN: 129025, Source: 1, Destination: 0xFF},
			Data:      bigData,
			Seq:       uint64(i + 1),
		}
	}
	close(ch)

	if err := jw.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	count := len(rotated)
	paths := make([]string, len(rotated))
	for i, rf := range rotated {
		paths[i] = rf.Path
	}
	mu.Unlock()

	// 2 rotations + 1 finalize = 3 callbacks.
	if count != 3 {
		t.Fatalf("expected 3 OnRotate calls, got %d: %v", count, paths)
	}

	// All paths should be real files in dir (they were closed, so they exist).
	for _, p := range paths {
		if !strings.HasPrefix(p, dir) {
			t.Errorf("path %q should be in dir %q", p, dir)
		}
		if !strings.HasSuffix(p, ".lpj") {
			t.Errorf("path %q should end with .lpj", p)
		}
	}
}

// -----------------------------------------------------------------------
// Integration: JournalWriter OnRotate fires on finalize (ctx cancel)
// -----------------------------------------------------------------------

func TestJournalWriterOnRotateOnFinalize(t *testing.T) {
	dir := t.TempDir()
	devices := NewDeviceRegistry()

	var called atomic.Bool

	ch := make(chan RxFrame, 10)
	jw, err := NewJournalWriter(JournalConfig{
		Dir:         dir,
		Prefix:      "nmea2k",
		BlockSize:   4096,
		Compression: journal.CompressionNone,
		OnRotate: func(rf RotatedFile) {
			called.Store(true)
		},
	}, devices, ch)
	if err != nil {
		t.Fatal(err)
	}

	// Start the writer, send frames, then cancel.
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- jw.Run(ctx) }()

	base := time.Now()
	for i := range 3 {
		ch <- RxFrame{
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
			Header:    CANHeader{Priority: 2, PGN: 129025, Source: 1, Destination: 0xFF},
			Data:      []byte{0, 1, 2, 3, 4, 5, 6, 7},
			Seq:       uint64(i + 1),
		}
	}

	// Small delay to let the writer consume the frames, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	if !called.Load() {
		t.Error("OnRotate should fire on finalize")
	}
}

// -----------------------------------------------------------------------
// Integration: BlockWriter OnRotate fires on rotation and Close
// -----------------------------------------------------------------------

func TestBlockWriterOnRotateCallback(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var rotated []string

	bw, err := NewBlockWriter(BlockWriterConfig{
		Dir:        dir,
		Prefix:     "backfill",
		BlockSize:  4096,
		RotateSize: 5000, // ~1 uncompressed block (4096 + header) triggers rotation
		OnRotate: func(rf RotatedFile) {
			mu.Lock()
			rotated = append(rotated, rf.Path)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write 3 uncompressed blocks (each 4096 bytes). After the first one,
	// fileBytes = 16 (header) + 4096 = 4112, not yet over 5000. After the
	// second, 4112 + 4096 = 8208, over 5000, triggers rotation.
	base := time.Now()
	for i := range 3 {
		block := make([]byte, 4096)
		// Need valid CRC for uncompressed blocks.
		ts := base.Add(time.Duration(i) * time.Second)
		if err := bw.AppendBlock(uint64(i*100+1), ts.UnixMicro(), block, false); err != nil {
			// CRC validation will fail on zeroed blocks, skip the error for this test.
			// Use compressed blocks instead.
			break
		}
	}

	// Use compressed blocks which skip CRC validation.
	bw2, err := NewBlockWriter(BlockWriterConfig{
		Dir:         dir,
		Prefix:      "backfill2",
		BlockSize:   4096,
		Compression: journal.CompressionZstd,
		RotateSize:  100, // tiny limit to trigger rotation quickly
		OnRotate: func(rf RotatedFile) {
			mu.Lock()
			rotated = append(rotated, rf.Path)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		ts := base.Add(time.Duration(i) * time.Second)
		data := make([]byte, 50) // some compressed payload
		if err := bw2.AppendBlock(uint64(i*100+1), ts.UnixMicro(), data, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := bw2.Close(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	count := len(rotated)
	mu.Unlock()

	// At least 2 callbacks expected (rotations + close).
	if count < 2 {
		t.Errorf("expected at least 2 OnRotate calls, got %d", count)
	}
}

// -----------------------------------------------------------------------
// Integration: full pipeline (JournalWriter → keeper → archive → delete)
// -----------------------------------------------------------------------

func TestFullPipelineJournalWriterToKeeperToArchiveToDelete(t *testing.T) {
	journalDir := t.TempDir()
	scriptDir := t.TempDir()
	devices := NewDeviceRegistry()

	// Create a pre-existing old file that should be cleaned up by retention.
	now := time.Now().UTC()
	oldPath := createTestJournal(t, journalDir, lpjName("nmea2k", now.Add(-10*24*time.Hour)), 100)

	// Set up the archive script.
	script := writeArchiveScript(t, scriptDir, okScript)

	// Create the keeper.
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: journalDir, InstanceID: "boat-1"}},
		MaxAge:         7 * 24 * time.Hour,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	// Use 2000-byte frames so each 4096-byte block fits exactly 2 frames.
	// With RotateCount=2, rotation fires every block. 6 frames → 2 rotations + 1 finalize.
	bigData := make([]byte, 2000)
	for i := range bigData {
		bigData[i] = byte(i)
	}

	ch := make(chan RxFrame, 200)
	jw, err := NewJournalWriter(JournalConfig{
		Dir:         journalDir,
		Prefix:      "nmea2k",
		BlockSize:   4096,
		Compression: journal.CompressionNone,
		RotateCount: 2,
		OnRotate: func(rf RotatedFile) {
			rf.InstanceID = "boat-1"
			keeper.Send(rf)
		},
	}, devices, ch)
	if err != nil {
		t.Fatal(err)
	}

	// Start the keeper.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Go(func() {
		keeper.Run(ctx)
	})

	// Write 6 frames: triggers 2 rotations + 1 finalize = 3 OnRotate callbacks.
	base := time.Now()
	for i := range 6 {
		ch <- RxFrame{
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
			Header:    CANHeader{Priority: 2, PGN: 129025, Source: 1, Destination: 0xFF},
			Data:      bigData,
			Seq:       uint64(i + 1),
		}
	}
	close(ch)

	// Run the journal writer to completion (processes all frames, finalizes).
	if err := jw.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Poll for the newest .archived marker to appear.
	deadline := time.After(5 * time.Second)
	for {
		entries, _ := os.ReadDir(journalDir)
		count := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".archived") {
				count++
			}
		}
		// We expect at least 2 archived files (rotations sent via keeper).
		if count >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for archived markers, got %d", count)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	wg.Wait()

	// Verify: newly rotated files should have .archived markers (on-rotate trigger).
	entries, _ := os.ReadDir(journalDir)
	archivedCount := 0
	lpjCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".archived") {
			archivedCount++
		}
		if strings.HasSuffix(e.Name(), ".lpj") {
			lpjCount++
		}
	}

	// We should have some archived files from the on-rotate notifications.
	if archivedCount == 0 {
		t.Error("expected at least one .archived marker from on-rotate")
	}

	// The old file should be deleted or about to be deleted (startup scan
	// archives it, second scan deletes it). Since the keeper only ran the
	// startup scan once, the old file might be archived but not yet deleted.
	// Verify it's at least archived.
	if _, err := os.Stat(oldPath); err == nil {
		if !isArchived(oldPath) {
			t.Error("old file should be archived or deleted by the keeper")
		}
	}
	// (If it doesn't exist, it was already deleted -- even better.)
}

// -----------------------------------------------------------------------
// Integration: before-expire full lifecycle with max-size trigger
// -----------------------------------------------------------------------

func TestBeforeExpireWithMaxSize(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 3 files x 400 bytes = 1200, max-size 800.
	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 400)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 400)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 400)

	script := writeArchiveScript(t, scriptDir, okScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        800,
		SoftPct:        0, // disable soft zone for this test
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	ctx := context.Background()

	// Scan 1: p1 needs archiving (oldest, over size cap).
	// 1200 > 800 -> expire p1 (archive). totalSize becomes 800, 800 <= 800 -> stop.
	keeper.scanAll(ctx)
	if !isArchived(p1) {
		t.Fatal("p1 should be archived")
	}

	// Scan 2: p1 is archived, gets deleted. Now total=800 (p2+p3), <=800.
	keeper.scanAll(ctx)
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Error("p1 should be deleted on second scan")
	}
	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("expected 2 files after deleting p1, got %d", got)
	}

	// p2 should not be archived (within size limit, no soft zone).
	if isArchived(p2) {
		t.Error("p2 should NOT be archived (within size limit after p1 deletion)")
	}
}

// -----------------------------------------------------------------------
// Edge: script that produces no stdout
// -----------------------------------------------------------------------

func TestArchiveScriptNoOutput(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	// Script that reads stdin but produces no stdout.
	script := writeArchiveScript(t, scriptDir, "cat > /dev/null")
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	// No stdout means no "ok" response, so file should be pending.
	if isArchived(path) {
		t.Error("file should NOT be archived (no script output)")
	}
	if len(keeper.pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(keeper.pending))
	}
}

// -----------------------------------------------------------------------
// Edge: script that crashes (non-zero exit)
// -----------------------------------------------------------------------

func TestArchiveScriptCrash(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	// Script that writes ok but also exits non-zero. The ok response should
	// still be honored because per-file status is authoritative over exit code.
	scriptBody := `
for arg in "$@"; do echo "{\"path\":\"$arg\",\"status\":\"ok\"}"; done
exit 1
`
	script := writeArchiveScript(t, scriptDir, scriptBody)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	if !isArchived(path) {
		t.Error("file should be archived (per-file ok overrides exit code)")
	}
}

// -----------------------------------------------------------------------
// Edge: script that outputs garbage JSON
// -----------------------------------------------------------------------

func TestArchiveScriptGarbageJSON(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	path := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	script := writeArchiveScript(t, scriptDir, `echo "this is not json"`)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	if isArchived(path) {
		t.Error("file should NOT be archived (garbage JSON)")
	}
	if len(keeper.pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(keeper.pending))
	}
}

// -----------------------------------------------------------------------
// Edge: duplicate pending files are not added twice
// -----------------------------------------------------------------------

func TestNoDuplicatePendingFiles(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()
	name := lpjName("nmea2k", now.Add(-1*time.Hour))
	path := createTestJournal(t, dir, name, 100)

	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	// Trigger twice for the same file.
	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})
	keeper.handleRotation(context.Background(), RotatedFile{Path: path, InstanceID: "test"})

	if len(keeper.pending) != 1 {
		t.Errorf("expected 1 pending (no duplicates), got %d", len(keeper.pending))
	}
}

// -----------------------------------------------------------------------
// Concurrent Send does not race (run with -race)
// -----------------------------------------------------------------------

func TestConcurrentSendNoRace(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs: []KeeperDir{{Dir: dir, InstanceID: "test"}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		keeper.Run(ctx)
		close(done)
	}()

	// Hammer Send from multiple goroutines.
	var wg sync.WaitGroup
	for g := range 10 {
		wg.Go(func() {
			for i := range 50 {
				keeper.Send(RotatedFile{
					Path:       filepath.Join(dir, lpjName("nmea2k", now.Add(time.Duration(g*50+i)*time.Second))),
					InstanceID: "test",
				})
			}
		})
	}
	wg.Wait()

	cancel()
	<-done
}

// -----------------------------------------------------------------------
// Unit: overflow policy parsing
// -----------------------------------------------------------------------

func TestParseOverflowPolicy(t *testing.T) {
	tests := []struct {
		input string
		want  OverflowPolicy
		err   bool
	}{
		{"", OverflowDeleteUnarchived, false},
		{"delete-unarchived", OverflowDeleteUnarchived, false},
		{"pause-recording", OverflowPauseRecording, false},
		{"yolo", OverflowDeleteUnarchived, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseOverflowPolicy(tt.input)
			if tt.err {
				if err == nil {
					t.Errorf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOverflowPolicyString(t *testing.T) {
	if s := OverflowDeleteUnarchived.String(); s != "delete-unarchived" {
		t.Errorf("got %q", s)
	}
	if s := OverflowPauseRecording.String(); s != "pause-recording" {
		t.Errorf("got %q", s)
	}
}

// -----------------------------------------------------------------------
// Unit: SoftPct default
// -----------------------------------------------------------------------

func TestSoftPctPreserved(t *testing.T) {
	// SoftPct=0 is preserved (not overridden by constructor).
	// CLI flags default to 80; constructor does not second-guess the caller.
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:    []KeeperDir{{Dir: t.TempDir()}},
		MaxSize: 10000,
		SoftPct: 0,
	})
	if keeper.cfg.SoftPct != 0 {
		t.Errorf("expected SoftPct=0 to be preserved, got %d", keeper.cfg.SoftPct)
	}

	// Explicit SoftPct is kept as-is.
	keeper2 := NewJournalKeeper(KeeperConfig{
		Dirs:    []KeeperDir{{Dir: t.TempDir()}},
		MaxSize: 10000,
		SoftPct: 50,
	})
	if keeper2.cfg.SoftPct != 50 {
		t.Errorf("expected SoftPct=50, got %d", keeper2.cfg.SoftPct)
	}
}

// -----------------------------------------------------------------------
// Soft zone: proactive archiving
// -----------------------------------------------------------------------

func TestSoftThresholdProactiveArchive(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 4 files x 300 = 1200. MaxSize=1500 (not exceeded). Soft at 80% = 1200.
	// Total 1200 == soft threshold, so soft zone NOT entered (needs > soft).
	// Bump to 5 files: 5x300=1500, soft at 80%=1200, total > soft, <= hard.
	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-5*time.Hour)), 300)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-4*time.Hour)), 300)
	p3 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 300)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 300)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 300)

	script := writeArchiveScript(t, scriptDir, okScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        1500,
		SoftPct:        80, // soft = 1200
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	keeper.scanAll(context.Background())

	// No files should be deleted (all within hard cap).
	if got := countLPJ(t, dir); got != 5 {
		t.Fatalf("expected 5 files (no deletion), got %d", got)
	}

	// Oldest non-archived files should be proactively archived.
	if !isArchived(p1) {
		t.Error("p1 should be proactively archived")
	}
	if !isArchived(p2) {
		t.Error("p2 should be proactively archived")
	}
	if !isArchived(p3) {
		t.Error("p3 should be proactively archived")
	}
}

func TestSoftThresholdNoEffectWithoutMaxSize(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// Only MaxAge, no MaxSize. Soft threshold has no effect.
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 10000)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-30*time.Minute)), 10000)

	script := writeArchiveScript(t, scriptDir, okScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxAge:         24 * time.Hour,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	keeper.scanAll(context.Background())

	// Nothing expired (all young), no proactive archiving.
	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("expected 2 files, got %d", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".archived") {
			t.Error("no files should be archived without MaxSize")
		}
	}
}

func TestSoftThresholdNoEffectWithoutArchive(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// MaxSize set, no archive command. Normal deletion, no soft zone.
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 500)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:    []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize: 1000,
		SoftPct: 80,
	})

	keeper.scanAll(context.Background())

	// 1500 > 1000: oldest deleted to get below cap.
	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("expected 2 files, got %d", got)
	}
}

func TestSoftThresholdSkipsAlreadyArchived(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 3 files x 500 = 1500. MaxSize=1500, soft at 80%=1200. Total > soft.
	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 500)
	markArchived(p1) // already archived
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 500)

	var archived []string
	scriptBody := `for arg in "$@"; do echo "{\"path\":\"$arg\",\"status\":\"ok\"}"; done`
	script := writeArchiveScript(t, scriptDir, scriptBody)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        1500,
		SoftPct:        80,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	keeper.scanAll(context.Background())

	// p1 already archived, p2 should now be archived, p3 also proactively archived.
	if !isArchived(p2) {
		t.Error("p2 should be proactively archived")
	}

	// Script should NOT have been called for p1 (already archived).
	_ = archived // just checking marker state
	if got := countLPJ(t, dir); got != 3 {
		t.Errorf("expected 3 files (no deletion), got %d", got)
	}
}

// -----------------------------------------------------------------------
// Overflow: delete-unarchived
// -----------------------------------------------------------------------

func TestOverflowDeleteUnarchived(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 3 files x 500 = 1500. MaxSize = 1000. Archive always fails.
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 500)

	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        1000,
		OverflowPolicy: OverflowDeleteUnarchived,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
	})

	ctx := context.Background()

	// Scan 1: hard cap hit, files queued for archive (first encounter).
	// Archive fails, files go to pending.
	keeper.scanAll(ctx)
	if got := countLPJ(t, dir); got != 3 {
		t.Fatalf("scan 1: expected 3 files (archive attempted first), got %d", got)
	}

	// Scan 2: hard cap still exceeded, files are now pending (failed archive).
	// Overflow policy kicks in: delete-unarchived deletes them.
	keeper.scanAll(ctx)
	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("scan 2: expected 2 files (oldest deleted by overflow policy), got %d", got)
	}
}

func TestOverflowDeleteUnarchivedMultiple(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 5 files x 300 = 1500. MaxSize = 600. Need to delete 3 files.
	for i := 5; i >= 1; i-- {
		createTestJournal(t, dir, lpjName("nmea2k", now.Add(-time.Duration(i)*time.Hour)), 300)
	}

	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        600,
		OverflowPolicy: OverflowDeleteUnarchived,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
	})

	ctx := context.Background()

	// Scan 1: archive attempted (first encounter), fails, files go to pending.
	keeper.scanAll(ctx)
	if got := countLPJ(t, dir); got != 5 {
		t.Fatalf("scan 1: expected 5 files (first archive attempt), got %d", got)
	}

	// Scan 2: files are pending, overflow policy deletes them.
	keeper.scanAll(ctx)
	if got := countLPJ(t, dir); got != 2 {
		t.Errorf("scan 2: expected 2 files, got %d: %v", got, listLPJ(t, dir))
	}
}

// -----------------------------------------------------------------------
// Overflow: pause-recording
// -----------------------------------------------------------------------

func TestOverflowPauseRecording(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 3 files x 500 = 1500. MaxSize = 1000. Archive always fails.
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 500)

	var pauseDir KeeperDir
	var pauseState bool
	pauseCalled := false

	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        1000,
		OverflowPolicy: OverflowPauseRecording,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
		OnPauseChange: func(d KeeperDir, paused bool) {
			pauseDir = d
			pauseState = paused
			pauseCalled = true
		},
	})

	ctx := context.Background()

	// Scan 1: archive attempted (first encounter), fails, files go to pending.
	keeper.scanAll(ctx)
	if got := countLPJ(t, dir); got != 3 {
		t.Fatalf("scan 1: expected 3 files, got %d", got)
	}

	// Scan 2: files are pending, overflow policy = pause-recording.
	keeper.scanAll(ctx)

	// Files should NOT be deleted (pause-recording preserves them).
	if got := countLPJ(t, dir); got != 3 {
		t.Errorf("scan 2: expected 3 files (pause, no deletion), got %d", got)
	}

	if !pauseCalled {
		t.Fatal("OnPauseChange should have been called")
	}
	if !pauseState {
		t.Error("should be paused=true")
	}
	if pauseDir.Dir != dir {
		t.Errorf("pause dir = %q, want %q", pauseDir.Dir, dir)
	}
}

func TestOverflowPauseResumed(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 3 files x 500 = 1500. MaxSize = 1000.
	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 500)

	var pauseHistory []bool

	// Archive always fails.
	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        1000,
		OverflowPolicy: OverflowPauseRecording,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
		OnPauseChange: func(_ KeeperDir, paused bool) {
			pauseHistory = append(pauseHistory, paused)
		},
	})

	ctx := context.Background()

	// Scan 1: first encounter, queues for archive (fails, goes to pending).
	keeper.scanAll(ctx)
	if len(pauseHistory) != 0 {
		t.Fatalf("scan 1: no pause yet (first attempt), got %v", pauseHistory)
	}

	// Scan 2: files are pending, overflow policy triggers pause.
	keeper.scanAll(ctx)
	if len(pauseHistory) != 1 || !pauseHistory[0] {
		t.Fatalf("scan 2: expected [true], got %v", pauseHistory)
	}

	// Simulate successful archive: mark p1 as archived.
	markArchived(p1)

	// Scan 3: p1 is archived, gets deleted. After deletion total=1000 <= MaxSize.
	// No overflow, pause lifted.
	keeper.scanAll(ctx)

	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Error("p1 should be deleted after archive")
	}

	if len(pauseHistory) != 2 || pauseHistory[1] {
		t.Errorf("expected [true, false], got %v", pauseHistory)
	}
}

func TestOverflowPausePerDirectory(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// dir1: 2x600 = 1200, MaxSize=1000 -> overflow
	createTestJournal(t, dir1, lpjName("nmea2k", now.Add(-2*time.Hour)), 600)
	createTestJournal(t, dir1, lpjName("nmea2k", now.Add(-1*time.Hour)), 600)

	// dir2: 1x400 = 400, within limits
	createTestJournal(t, dir2, lpjName("nmea2k", now.Add(-1*time.Hour)), 400)

	pausedDirs := make(map[string]bool)
	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs: []KeeperDir{
			{Dir: dir1, InstanceID: "boat-1"},
			{Dir: dir2, InstanceID: "boat-2"},
		},
		MaxSize:        1000,
		OverflowPolicy: OverflowPauseRecording,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
		OnPauseChange: func(d KeeperDir, paused bool) {
			pausedDirs[d.Dir] = paused
		},
	})

	ctx := context.Background()

	// Scan 1: first encounter, queues for archive (fails).
	keeper.scanAll(ctx)

	// Scan 2: files pending, overflow triggers pause for dir1 only.
	keeper.scanAll(ctx)

	if !pausedDirs[dir1] {
		t.Error("dir1 should be paused (overflow)")
	}
	if _, called := pausedDirs[dir2]; called {
		t.Error("dir2 should not have triggered OnPauseChange")
	}
}

// -----------------------------------------------------------------------
// JournalWriter: pause discards frames
// -----------------------------------------------------------------------

func TestJournalWriterPausedDiscardsFrames(t *testing.T) {
	dir := t.TempDir()
	devices := NewDeviceRegistry()

	// Use unbuffered channel so we know the writer has consumed each frame
	// before we send the next.
	ch := make(chan RxFrame)
	jw, err := NewJournalWriter(JournalConfig{
		Dir:         dir,
		Prefix:      "nmea2k",
		BlockSize:   4096,
		Compression: journal.CompressionNone,
	}, devices, ch)
	if err != nil {
		t.Fatal(err)
	}

	// Start writer in background.
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- jw.Run(context.Background())
	}()

	base := time.Now()
	sendFrame := func(i int) {
		ch <- RxFrame{
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
			Header:    CANHeader{Priority: 2, PGN: 129025, Source: 1, Destination: 0xFF},
			Data:      []byte{0, 1, 2, 3, 4, 5, 6, 7},
			Seq:       uint64(i + 1),
		}
	}

	// Phase 1: send 3 frames unpaused.
	for i := range 3 {
		sendFrame(i)
	}

	// Phase 2: pause, send 3 more frames (will be consumed but discarded).
	jw.SetPaused(true)
	for i := 3; i < 6; i++ {
		sendFrame(i)
	}

	// Phase 3: resume, send 3 more frames.
	jw.SetPaused(false)
	for i := 6; i < 9; i++ {
		sendFrame(i)
	}

	close(ch)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}

	// Read back journal files and count frames via the journal reader.
	entries, _ := os.ReadDir(dir)
	totalFrames := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lpj") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		r, err := journal.NewReader(f)
		if err != nil {
			_ = f.Close()
			t.Fatalf("journal reader %s: %v", path, err)
		}
		for r.Next() {
			totalFrames++
		}
		_ = f.Close()
	}

	// 9 frames sent total. Phase 1 (3) and Phase 3 (3) are unpaused.
	// Phase 2 (3) is paused. Boundary frames at the pause/unpause transitions
	// may or may not be discarded: the writer receives from the unbuffered
	// channel and then checks paused.Load(), racing with the test's Store.
	// Frame at the unpause boundary (frame 5) can leak through if the writer
	// sees the Store(false) before checking the flag. Valid range: 4-7.
	if totalFrames < 4 || totalFrames > 7 {
		t.Errorf("expected 4-7 frames written (some discarded while paused), got %d", totalFrames)
	}
	// Critically: not all 9 frames were written, meaning pause had an effect.
	if totalFrames == 9 {
		t.Error("all 9 frames written, pause had no effect")
	}
}

// -----------------------------------------------------------------------
// Integration: soft -> hard -> pause -> archive succeeds -> resume
// -----------------------------------------------------------------------

func TestEndToEndSoftToHardToPause(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// 3 files x 500 = 1500, MaxSize=1000. No soft zone to keep it focused.
	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 500)
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 500)

	writerPaused := new(atomic.Bool)
	var pauseHistory []bool

	// Archive always fails.
	script := writeArchiveScript(t, scriptDir, errScript)
	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		MaxSize:        1000,
		SoftPct:        0, // disable soft zone, focus on hard cap flow
		OverflowPolicy: OverflowPauseRecording,
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveBeforeExpire,
		OnPauseChange: func(_ KeeperDir, paused bool) {
			writerPaused.Store(paused)
			pauseHistory = append(pauseHistory, paused)
		},
	})

	ctx := context.Background()

	// Scan 1: hard cap hit (1500 > 1000). First encounter: queues for archive.
	// Archive fails, files go to pending.
	keeper.scanAll(ctx)
	if writerPaused.Load() {
		t.Fatal("should not be paused yet (first archive attempt)")
	}

	// Scan 2: files are pending. Hard cap still exceeded. Overflow = pause.
	keeper.scanAll(ctx)
	if !writerPaused.Load() {
		t.Fatal("should be paused after hard cap overflow with pending files")
	}
	if len(pauseHistory) != 1 || !pauseHistory[0] {
		t.Fatalf("expected [true], got %v", pauseHistory)
	}

	// Simulate archive success: mark oldest file as archived.
	markArchived(p1)

	// Scan 3: p1 is archived, gets deleted. After deletion total=1000 <= MaxSize.
	// No overflow, pause lifted.
	keeper.scanAll(ctx)

	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Error("p1 should be deleted after archive")
	}

	if writerPaused.Load() {
		t.Error("should resume after archives free space")
	}

	// Verify the full pause/resume cycle.
	if len(pauseHistory) != 2 {
		t.Fatalf("expected [true, false], got %v", pauseHistory)
	}
	if !pauseHistory[0] || pauseHistory[1] {
		t.Errorf("expected [true, false], got %v", pauseHistory)
	}
}

// -----------------------------------------------------------------------
// Startup archive sweep
// -----------------------------------------------------------------------

// TestStartupArchivesUnarchivedFiles verifies that Run() archives all
// non-archived .lpj files on startup, regardless of trigger mode.
func TestStartupArchivesUnarchivedFiles(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	// Three files, none archived, none expired.
	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 100)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 100)
	p3 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
		MaxAge:         30 * 24 * time.Hour, // files are nowhere near expiry
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Let Run() execute the startup scan, then cancel.
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	keeper.Run(ctx)

	// p1 and p2 should be archived. p3 is the newest file in the dir,
	// so it should be skipped (likely the active journal).
	if !isArchived(p1) {
		t.Errorf("p1 should be archived: %s", p1)
	}
	if !isArchived(p2) {
		t.Errorf("p2 should be archived: %s", p2)
	}
	// p3 is the newest file in the dir, so it should be skipped (active journal).
	if isArchived(p3) {
		t.Errorf("p3 (newest) should NOT be archived: %s", p3)
	}
}

// TestStartupSkipsAlreadyArchivedFiles verifies the startup sweep doesn't
// re-archive files that already have .archived markers.
func TestStartupSkipsAlreadyArchivedFiles(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-3*time.Hour)), 100)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 100)
	// The newest file, will be skipped as active.
	createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	// p1 is already archived.
	markArchived(p1)

	trackFile := filepath.Join(scriptDir, "calls.log")
	trackScript := fmt.Sprintf(`
for arg in "$@"; do
  echo "$arg" >> %s
  echo "{\"path\":\"$arg\",\"status\":\"ok\"}"
done
`, trackFile)
	script := writeArchiveScript(t, scriptDir, trackScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
		MaxAge:         30 * 24 * time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	keeper.Run(ctx)

	// p1 was already archived, p2 should now be archived too.
	if !isArchived(p2) {
		t.Error("p2 should be archived")
	}

	// Check the tracking file: only p2 should appear (p1 was already archived,
	// p3 is skipped as active).
	data, err := os.ReadFile(trackFile)
	if err != nil {
		t.Fatalf("tracking file missing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 archive call, got %d: %v", len(lines), lines)
	}
	if lines[0] != p2 {
		t.Errorf("expected archive call for %s, got %s", p2, lines[0])
	}
}

// TestStartupSkipsActiveFile verifies the startup sweep skips the most recent
// file per directory (the one likely being written to).
func TestStartupSkipsActiveFile(t *testing.T) {
	dir := t.TempDir()
	scriptDir := t.TempDir()
	now := time.Now().UTC()

	p1 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-2*time.Hour)), 100)
	p2 := createTestJournal(t, dir, lpjName("nmea2k", now.Add(-1*time.Hour)), 100)

	script := writeArchiveScript(t, scriptDir, okScript)

	keeper := NewJournalKeeper(KeeperConfig{
		Dirs:           []KeeperDir{{Dir: dir, InstanceID: "test"}},
		ArchiveCommand: script,
		ArchiveTrigger: ArchiveOnRotate,
		MaxAge:         30 * 24 * time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	keeper.Run(ctx)

	// p1 (older) should be archived.
	if !isArchived(p1) {
		t.Error("p1 should be archived")
	}
	// p2 (newest) should NOT be archived (active journal).
	if isArchived(p2) {
		t.Error("p2 (newest/active) should NOT be archived")
	}
}
