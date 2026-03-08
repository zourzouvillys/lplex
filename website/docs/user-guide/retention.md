---
sidebar_position: 5
title: Retention & Archival
---

# Retention & Archival

The `JournalKeeper` manages automatic cleanup and archival of journal files. It runs as a background goroutine in both `lplex` and `lplex-cloud`.

## Retention policy

Three knobs control when files are deleted:

| Setting | Flag | Description |
|---|---|---|
| Max age | `-journal-retention-max-age` | Delete files older than this (e.g., `P30D`) |
| Min keep | `-journal-retention-min-keep` | Keep at least this much data, even if over max-age |
| Max size | `-journal-retention-max-size` | Hard cap on total journal size in bytes |

**Priority**: max-size overrides min-keep overrides max-age.

Files are evaluated oldest-first. Once a file is kept, all newer files are also kept.

### Example

With `max-age=P30D`, `min-keep=PT24H`, `max-size=10GB`:

- Files older than 30 days are expired (max-age)
- But at least 24 hours of data is always kept (min-keep)
- If total size exceeds 10 GB, files are deleted starting from oldest, even if within min-keep (max-size)

## Soft/hard thresholds

When `max-size` and archival are both configured, a soft/hard threshold system kicks in:

```
0%                  soft-pct%              100%
|---- normal ------|----- soft zone ------|--- hard zone --->
                   (proactive archiving)   (enforce policy)
```

The `soft-pct` setting (default 80) defines the soft threshold as a percentage of `max-size`.

| Zone | Condition | Behavior |
|---|---|---|
| Normal | total ≤ soft | Standard age-based expiration, archive-then-delete |
| Soft | soft &lt; total ≤ hard | Proactively queue oldest non-archived files for archival |
| Hard | total &gt; hard | Apply overflow policy |

### Overflow policy

When the hard cap is hit and archives have failed:

| Policy | Flag value | Behavior |
|---|---|---|
| Delete unarchived | `delete-unarchived` | Delete files even if not archived (prioritizes continued recording) |
| Pause recording | `pause-recording` | Stop writing new journal data (prioritizes archive completeness) |

Default is `delete-unarchived`. The `pause-recording` policy propagates via `OnPauseChange` callbacks to pause the JournalWriter.

## Archival

Archival sends journal files to an external system (S3, GCS, etc.) via a user-provided script.

### Configuration

```hocon
journal {
  archive {
    command = /usr/local/bin/archive-to-s3
    trigger = on-rotate
  }
}
```

### Trigger modes

| Trigger | Description |
|---|---|
| `on-rotate` | Archive immediately after a file is rotated (closed) |
| `before-expire` | Archive only when a file is about to be deleted by retention |

### Script protocol

The archive script receives file paths as arguments and metadata on stdin as JSONL. It writes per-file results to stdout as JSONL.

**stdin** (one JSON object per line, one per file):
```json
{"path": "/var/log/lplex/nmea2k-20260306T101500Z.lpj", "size": 2621440}
```

**stdout** (one JSON object per line, one per file):
```json
{"path": "/var/log/lplex/nmea2k-20260306T101500Z.lpj", "status": "ok"}
```

On failure:
```json
{"path": "/var/log/lplex/nmea2k-20260306T101500Z.lpj", "status": "error", "error": "upload failed: connection timeout"}
```

### Marker files

Successfully archived files get a zero-byte `.archived` sidecar marker:

```
nmea2k-20260306T101500Z.lpj
nmea2k-20260306T101500Z.lpj.archived
```

The keeper uses these markers to track archive state across restarts.

### Startup archive sweep

When the archive trigger is `on-rotate`, the keeper runs a one-time sweep on startup to archive any `.lpj` files that are missing their `.archived` marker. This runs before any brokers start, so all files on disk are completed files from previous runs. This catches files that were rotated but never archived, for example if the process was restarted before the `on-rotate` callback fired.

### Retry behavior

Failed archives retry with exponential backoff: 1 minute initial delay, doubling up to a 1 hour cap.

## Cloud considerations

In `lplex-cloud`, a single JournalKeeper goroutine manages all instance directories. The `InstanceManager` threads `OnRotate` callbacks to each instance's JournalWriter and BlockWriter. The `DirFunc` dynamically discovers instance journal directories so the keeper adapts as instances come and go.
