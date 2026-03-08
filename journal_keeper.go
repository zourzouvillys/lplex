package lplex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RotatedFile describes a completed journal file.
type RotatedFile struct {
	Path       string
	InstanceID string
}

// ArchiveTrigger controls when journal files are archived.
type ArchiveTrigger int

const (
	ArchiveDisabled     ArchiveTrigger = iota
	ArchiveOnRotate                    // archive immediately after rotation
	ArchiveBeforeExpire                // archive only when about to be deleted by retention
)

// OverflowPolicy controls what happens when the hard size cap is hit
// and files haven't been archived yet.
type OverflowPolicy int

const (
	OverflowDeleteUnarchived OverflowPolicy = iota // delete files even if not archived
	OverflowPauseRecording                         // stop journal writes until archives free space
)

// KeeperDir is a directory managed by the keeper.
type KeeperDir struct {
	Dir        string
	InstanceID string
}

// KeeperConfig configures the JournalKeeper.
type KeeperConfig struct {
	// Dirs to manage. Ignored if DirFunc is set.
	Dirs []KeeperDir

	// DirFunc returns current directories on each scan cycle.
	// Used by cloud to dynamically discover instance dirs.
	DirFunc func() []KeeperDir

	// Retention
	MaxAge  time.Duration // 0 = no age limit
	MinKeep time.Duration // 0 = no min-keep floor
	MaxSize int64         // 0 = no size limit

	// Soft/hard thresholds
	SoftPct        int            // % of MaxSize for proactive archiving (default 80, range 1-99)
	OverflowPolicy OverflowPolicy // what to do when hard cap hit with non-archived files

	// Archive
	ArchiveCommand string         // path to script; empty = no archiving
	ArchiveTrigger ArchiveTrigger // on-rotate or before-expire

	// Pause callback (called when overflow-policy=pause-recording toggles state)
	OnPauseChange func(dir KeeperDir, paused bool)

	Logger *slog.Logger
}

func (c *KeeperConfig) hasArchive() bool {
	return c.ArchiveCommand != "" && c.ArchiveTrigger != ArchiveDisabled
}

// archiveRequest is the JSONL metadata sent to the archive script on stdin.
type archiveRequest struct {
	Path       string `json:"path"`
	InstanceID string `json:"instance_id"`
	Size       int64  `json:"size"`
	Created    string `json:"created"`
}

// archiveResponse is the JSONL result read from the archive script's stdout.
type archiveResponse struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "ok" or "error"
	Error  string `json:"error,omitempty"`
}

// pendingFile is a file waiting to be archived.
type pendingFile struct {
	path       string
	instanceID string
	size       int64
	created    time.Time
}

// JournalKeeper manages journal file archival and retention. One goroutine
// per binary, driven by rotation notifications and periodic directory scans.
type JournalKeeper struct {
	cfg      KeeperConfig
	rotateCh chan RotatedFile
	logger   *slog.Logger

	// archive retry state
	pending  []pendingFile
	backoff  time.Duration

	// per-directory pause state (overflow policy = pause-recording)
	paused map[string]bool
}

const (
	keeperScanInterval   = 5 * time.Minute
	keeperInitialBackoff = 1 * time.Minute
	keeperMaxBackoff     = 1 * time.Hour
	keeperScriptTimeout  = 5 * time.Minute
)

// NewJournalKeeper creates a keeper. Call Run to start.
func NewJournalKeeper(cfg KeeperConfig) *JournalKeeper {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &JournalKeeper{
		cfg:      cfg,
		rotateCh: make(chan RotatedFile, 64),
		logger:   cfg.Logger.With("component", "keeper"),
		paused:   make(map[string]bool),
	}
}

// SetOnPauseChange sets the callback invoked when a directory's pause state
// transitions. Must be called before Run.
func (k *JournalKeeper) SetOnPauseChange(fn func(dir KeeperDir, paused bool)) {
	k.cfg.OnPauseChange = fn
}

// Send notifies the keeper that a file was rotated. Non-blocking; drops if
// the channel is full (the periodic scan will pick it up).
func (k *JournalKeeper) Send(rf RotatedFile) {
	select {
	case k.rotateCh <- rf:
	default:
		k.logger.Warn("rotate notification dropped (channel full), will pick up on next scan", "path", rf.Path)
	}
}

// Run is the main loop. Blocks until ctx is cancelled.
func (k *JournalKeeper) Run(ctx context.Context) {
	// Startup scan: handle files rotated while we were down.
	k.scanAll(ctx)

	// Archive any .lpj files that were rotated but never archived (e.g. process
	// crashed before on-rotate fired, or files accumulated before archiving was
	// configured). Runs once on startup regardless of trigger mode.
	k.archiveUnarchived(ctx)

	scanTicker := time.NewTicker(keeperScanInterval)
	defer scanTicker.Stop()

	var retryTimer *time.Timer
	var retryCh <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			// Drain any rotation notifications that arrived between
			// im.Shutdown() and cancel(). Without this, files finalized
			// during shutdown would never get archived.
			for {
				select {
				case rf := <-k.rotateCh:
					k.handleRotation(context.Background(), rf)
				default:
					return
				}
			}

		case rf := <-k.rotateCh:
			k.handleRotation(ctx, rf)

		case <-scanTicker.C:
			k.scanAll(ctx)

		case <-retryCh:
			k.retryArchive(ctx)
			retryTimer = nil
			retryCh = nil
		}

		// Arm retry timer if we have pending files and no timer is running.
		if len(k.pending) > 0 && retryTimer == nil {
			if k.backoff == 0 {
				k.backoff = keeperInitialBackoff
			}
			retryTimer = time.NewTimer(k.backoff)
			retryCh = retryTimer.C
		}
	}
}

// handleRotation processes a single rotation notification.
func (k *JournalKeeper) handleRotation(ctx context.Context, rf RotatedFile) {
	if k.cfg.ArchiveTrigger != ArchiveOnRotate {
		return
	}

	info, err := os.Stat(rf.Path)
	if err != nil {
		k.logger.Warn("rotated file stat failed", "path", rf.Path, "error", err)
		return
	}

	created := parseTimestampFromFilename(filepath.Base(rf.Path))
	pf := pendingFile{
		path:       rf.Path,
		instanceID: rf.InstanceID,
		size:       info.Size(),
		created:    created,
	}

	k.archiveFiles(ctx, []pendingFile{pf})
}

// archiveUnarchived is a one-shot startup sweep that archives any .lpj files
// missing their .archived sidecar marker. The most recent file in each directory
// is skipped because it's likely the active journal being written to.
func (k *JournalKeeper) archiveUnarchived(ctx context.Context) {
	if k.cfg.ArchiveCommand == "" {
		return
	}

	dirs := k.cfg.Dirs
	if k.cfg.DirFunc != nil {
		dirs = k.cfg.DirFunc()
	}

	var toArchive []pendingFile
	for _, d := range dirs {
		if ctx.Err() != nil {
			return
		}

		entries, err := os.ReadDir(d.Dir)
		if err != nil {
			continue
		}

		// Collect .lpj files sorted by timestamp (oldest first).
		var files []journalFileInfo
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".lpj") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			path := filepath.Join(d.Dir, name)
			files = append(files, journalFileInfo{
				path:       path,
				instanceID: d.InstanceID,
				size:       info.Size(),
				created:    parseTimestampFromFilename(name),
				archived:   isArchived(path),
			})
		}

		slices.SortFunc(files, func(a, b journalFileInfo) int {
			return a.created.Compare(b.created)
		})

		if len(files) == 0 {
			continue
		}

		// Skip the newest file (likely the active journal).
		candidates := files[:len(files)-1]

		for _, f := range candidates {
			if !f.archived && !k.isPending(f.path) {
				toArchive = append(toArchive, makePending(f))
			}
		}
	}

	if len(toArchive) > 0 {
		k.logger.Info("startup archive sweep", "files", len(toArchive))
		k.archiveFiles(ctx, toArchive)
	}
}

// scanAll scans all configured directories and applies retention + archive rules.
func (k *JournalKeeper) scanAll(ctx context.Context) {
	dirs := k.cfg.Dirs
	if k.cfg.DirFunc != nil {
		dirs = k.cfg.DirFunc()
	}

	for _, d := range dirs {
		if ctx.Err() != nil {
			return
		}
		k.scanDir(ctx, d)
	}
}

// journalFileInfo holds metadata about a journal file for retention decisions.
type journalFileInfo struct {
	path       string
	instanceID string
	size       int64
	created    time.Time
	archived   bool
}

// scanDir scans a single directory and applies retention + archive rules.
func (k *JournalKeeper) scanDir(ctx context.Context, d KeeperDir) {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		if !os.IsNotExist(err) {
			k.logger.Warn("scan dir failed", "dir", d.Dir, "error", err)
		}
		return
	}

	now := time.Now()
	var files []journalFileInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".lpj") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, journalFileInfo{
			path:       filepath.Join(d.Dir, name),
			instanceID: d.InstanceID,
			size:       info.Size(),
			created:    parseTimestampFromFilename(name),
			archived:   isArchived(filepath.Join(d.Dir, name)),
		})
	}

	// Sort oldest first by filename timestamp.
	slices.SortFunc(files, func(a, b journalFileInfo) int {
		return a.created.Compare(b.created)
	})

	// Calculate total size.
	var totalSize int64
	for _, f := range files {
		totalSize += f.size
	}

	// Compute soft threshold for proactive archiving.
	// Only meaningful when MaxSize > 0, archive is configured, and SoftPct > 0.
	var softThreshold int64
	if k.cfg.MaxSize > 0 && k.cfg.hasArchive() && k.cfg.SoftPct > 0 {
		softThreshold = k.cfg.MaxSize * int64(k.cfg.SoftPct) / 100
	}

	var toArchive []pendingFile
	needPause := false

	for i, f := range files {
		// --- Phase 1: Hard expiration ---
		hardExpire := false

		if k.cfg.MaxSize > 0 && totalSize > k.cfg.MaxSize {
			hardExpire = true // size cap overrides everything
		} else if k.cfg.MaxAge > 0 {
			age := now.Sub(f.created)
			if k.cfg.MinKeep > 0 {
				hardExpire = age > k.cfg.MaxAge && age > k.cfg.MinKeep
			} else {
				hardExpire = age > k.cfg.MaxAge
			}
		}

		if hardExpire {
			if k.cfg.hasArchive() && !f.archived {
				if k.cfg.MaxSize > 0 && totalSize > k.cfg.MaxSize && k.isPending(f.path) {
					// Hard cap with file that already failed to archive:
					// apply overflow policy.
					switch k.cfg.OverflowPolicy {
					case OverflowDeleteUnarchived:
						k.deleteFile(f.path)
					case OverflowPauseRecording:
						needPause = true
					}
				} else if !k.isPending(f.path) {
					// First encounter or age-based: queue for archive.
					toArchive = append(toArchive, makePending(f))
				}
			} else {
				k.deleteFile(f.path)
			}
			totalSize -= f.size
			continue
		}

		// --- Phase 2: Soft zone proactive archiving ---
		if softThreshold == 0 || totalSize <= softThreshold {
			break // below soft threshold (or no threshold), done
		}

		// In soft zone: proactively archive non-archived files (don't delete).
		for _, sf := range files[i:] {
			if !sf.archived && !k.isPending(sf.path) {
				toArchive = append(toArchive, makePending(sf))
			}
		}
		break
	}

	if len(toArchive) > 0 {
		k.archiveFiles(ctx, toArchive)
	}

	// Update per-directory pause state.
	wasPaused := k.paused[d.Dir]
	if needPause != wasPaused {
		k.paused[d.Dir] = needPause
		if k.cfg.OnPauseChange != nil {
			k.cfg.OnPauseChange(d, needPause)
		}
	}
}

// isPending returns true if a file path is already in the pending retry queue.
func (k *JournalKeeper) isPending(path string) bool {
	for _, p := range k.pending {
		if p.path == path {
			return true
		}
	}
	return false
}

// makePending converts a journalFileInfo into a pendingFile for archive queueing.
func makePending(f journalFileInfo) pendingFile {
	return pendingFile{
		path:       f.path,
		instanceID: f.instanceID,
		size:       f.size,
		created:    f.created,
	}
}

// archiveFiles runs the archive script for a batch of files.
func (k *JournalKeeper) archiveFiles(ctx context.Context, files []pendingFile) {
	if len(files) == 0 || k.cfg.ArchiveCommand == "" {
		return
	}

	results := k.runScript(ctx, files)

	var failed []pendingFile
	for i, pf := range files {
		if i < len(results) && results[i].Status == "ok" {
			markArchived(pf.path)
			k.logger.Info("archived", "path", pf.path)
		} else {
			errMsg := "no response from script"
			if i < len(results) && results[i].Error != "" {
				errMsg = results[i].Error
			}
			k.logger.Warn("archive failed", "path", pf.path, "error", errMsg)
			if !k.isPending(pf.path) {
				failed = append(failed, pf)
			}
		}
	}

	if len(failed) > 0 {
		k.pending = append(k.pending, failed...)
	} else if len(k.pending) == 0 {
		// All succeeded and nothing pending: reset backoff.
		k.backoff = 0
	}
}

// retryArchive retries all pending failed files in one batch.
func (k *JournalKeeper) retryArchive(ctx context.Context) {
	if len(k.pending) == 0 {
		return
	}

	// Filter out files that no longer exist (may have been manually deleted).
	alive := k.pending[:0]
	for _, pf := range k.pending {
		if _, err := os.Stat(pf.path); err == nil {
			alive = append(alive, pf)
		}
	}
	k.pending = alive

	if len(k.pending) == 0 {
		k.backoff = 0
		return
	}

	k.logger.Info("retrying archive", "files", len(k.pending), "backoff", k.backoff)

	results := k.runScript(ctx, k.pending)

	var stillFailed []pendingFile
	for i, pf := range k.pending {
		if i < len(results) && results[i].Status == "ok" {
			markArchived(pf.path)
			k.logger.Info("archived (retry)", "path", pf.path)
		} else {
			stillFailed = append(stillFailed, pf)
		}
	}

	k.pending = stillFailed

	if len(k.pending) == 0 {
		k.backoff = 0
	} else {
		// Exponential backoff.
		k.backoff *= 2
		if k.backoff > keeperMaxBackoff {
			k.backoff = keeperMaxBackoff
		}
	}
}

// runScript executes the archive command with file paths as args and JSONL
// metadata on stdin. Returns per-file responses parsed from stdout.
func (k *JournalKeeper) runScript(ctx context.Context, files []pendingFile) []archiveResponse {
	ctx, cancel := context.WithTimeout(ctx, keeperScriptTimeout)
	defer cancel()

	args := make([]string, len(files))
	for i, f := range files {
		args[i] = f.path
	}

	cmd := exec.CommandContext(ctx, k.cfg.ArchiveCommand, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		k.logger.Error("archive script stdin pipe failed", "error", err)
		return nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		k.logger.Error("archive script stdout pipe failed", "error", err)
		return nil
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		k.logger.Error("archive script start failed", "error", err, "command", k.cfg.ArchiveCommand)
		return nil
	}

	// Write JSONL metadata to stdin.
	go func() {
		enc := json.NewEncoder(stdin)
		for _, f := range files {
			_ = enc.Encode(archiveRequest{
				Path:       f.path,
				InstanceID: f.instanceID,
				Size:       f.size,
				Created:    f.created.UTC().Format(time.RFC3339),
			})
		}
		_ = stdin.Close()
	}()

	// Read JSONL responses from stdout.
	var responses []archiveResponse
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var resp archiveResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			k.logger.Warn("archive script invalid JSON response", "line", scanner.Text(), "error", err)
			continue
		}
		responses = append(responses, resp)
	}

	if err := cmd.Wait(); err != nil {
		k.logger.Warn("archive script exited with error", "error", err)
	}

	return responses
}

// deleteFile removes a journal file and its .archived marker.
func (k *JournalKeeper) deleteFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		k.logger.Warn("delete failed", "path", path, "error", err)
		return
	}
	// Clean up marker file too.
	_ = os.Remove(path + ".archived")
	k.logger.Info("deleted", "path", path)
}

// isArchived checks if the sidecar marker file exists.
func isArchived(path string) bool {
	_, err := os.Stat(path + ".archived")
	return err == nil
}

// markArchived creates the zero-byte sidecar marker file.
func markArchived(path string) {
	f, err := os.Create(path + ".archived")
	if err != nil {
		return
	}
	_ = f.Close()
}

// parseTimestampFromFilename extracts the timestamp from a journal filename
// like "nmea2k-20260302T140000.000Z.lpj" or "backfill-20260302T140000.000Z.lpj".
func parseTimestampFromFilename(name string) time.Time {
	// Strip extension.
	name = strings.TrimSuffix(name, ".lpj")

	// Find the timestamp part after the last hyphen before the date.
	// Format: {prefix}-{timestamp}
	idx := strings.LastIndex(name, "-")
	if idx < 0 || idx+1 >= len(name) {
		return time.Time{}
	}
	tsPart := name[idx+1:]

	// Parse "20060102T150405.000Z"
	t, err := time.Parse("20060102T150405.000Z", tsPart)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ParseArchiveTrigger parses a string into an ArchiveTrigger.
func ParseArchiveTrigger(s string) (ArchiveTrigger, error) {
	switch s {
	case "", "disabled":
		return ArchiveDisabled, nil
	case "on-rotate":
		return ArchiveOnRotate, nil
	case "before-expire":
		return ArchiveBeforeExpire, nil
	default:
		return ArchiveDisabled, fmt.Errorf("unknown archive trigger: %q (want on-rotate or before-expire)", s)
	}
}

// String returns the string representation of an ArchiveTrigger.
func (t ArchiveTrigger) String() string {
	switch t {
	case ArchiveOnRotate:
		return "on-rotate"
	case ArchiveBeforeExpire:
		return "before-expire"
	default:
		return "disabled"
	}
}

// ParseOverflowPolicy parses a string into an OverflowPolicy.
func ParseOverflowPolicy(s string) (OverflowPolicy, error) {
	switch s {
	case "", "delete-unarchived":
		return OverflowDeleteUnarchived, nil
	case "pause-recording":
		return OverflowPauseRecording, nil
	default:
		return OverflowDeleteUnarchived, fmt.Errorf("unknown overflow policy: %q (want delete-unarchived or pause-recording)", s)
	}
}

// String returns the string representation of an OverflowPolicy.
func (p OverflowPolicy) String() string {
	switch p {
	case OverflowPauseRecording:
		return "pause-recording"
	default:
		return "delete-unarchived"
	}
}
