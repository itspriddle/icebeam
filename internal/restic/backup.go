package restic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// BackupSummary holds the totals restic reports at the end of a `backup --json`
// run. The fields mirror restic's "summary" message; zero values are returned
// when the field is absent or the summary could not be parsed.
type BackupSummary struct {
	// FilesNew is the number of files added since the previous snapshot.
	FilesNew int `json:"files_new"`
	// FilesChanged is the number of files modified since the previous snapshot.
	FilesChanged int `json:"files_changed"`
	// FilesUnmodified is the number of files unchanged since the previous snapshot.
	FilesUnmodified int `json:"files_unmodified"`
	// TotalFilesProcessed is the total number of files the backup scanned.
	TotalFilesProcessed int `json:"total_files_processed"`
	// TotalBytesProcessed is the total number of bytes the backup scanned.
	TotalBytesProcessed uint64 `json:"total_bytes_processed"`
	// DataAdded is the number of bytes added to the repository.
	DataAdded uint64 `json:"data_added"`
	// SnapshotID is the id of the snapshot the backup created.
	SnapshotID string `json:"snapshot_id"`
}

// resticMessage is the envelope restic wraps each `--json` line in. Only the
// summary line carries the totals we surface; status/error lines are streamed
// to the logger like any other restic output.
//
// StructType is the equivalent field restic <0.17.0 used for `ls --json` before
// Enhancement #4664 added message_type there; it is read as a fallback so `ls`
// listings still parse on older restic (see parseLSStream). On every other
// command, and on restic 0.17.0+, message_type is authoritative.
type resticMessage struct {
	MessageType string `json:"message_type"`
	StructType  string `json:"struct_type"`
}

// Backup runs `restic backup` with --json, streaming restic's per-file/status
// output to the logger and returning the final summary. args is the backup
// argument vector (paths and flags) without the leading subcommand or --json,
// e.g. {"/data", "--tag", "home", "--exclude", "**/node_modules"}.
//
// restic's backup --json emits a stream of newline-delimited JSON objects; the
// final object is a "summary" carrying the file/byte totals. A non-zero exit is
// reported as an *ExitError (so an incomplete backup, code 3, is distinguishable
// from a hard failure); the summary parsed so far is still returned so callers
// can report partial progress.
func (r *Runner) Backup(ctx context.Context, args ...string) (*BackupSummary, error) {
	backupArgs := append([]string{"backup", "--json"}, args...)

	cmd, err := r.command(ctx, backupArgs)
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("restic: pipe stdout: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("restic: pipe stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("restic: start backup: %w", err)
	}

	// restic writes the JSON stream to stdout; drain stderr to the logger so
	// progress/errors stay visible without polluting the JSON parse. The
	// goroutine returns when stderr closes (on process exit).
	tail := &outputTail{}
	stderrDone := make(chan struct{})
	go func() {
		r.streamOutput(stderr, "backup", tail)
		close(stderrDone)
	}()

	summary := r.parseBackupStream(stdout)
	<-stderrDone

	return summary, r.wait(ctx, cmd, backupArgs, tail)
}

// parseBackupStream reads restic's newline-delimited backup JSON, capturing the
// final summary message and logging every other line. A line that is not the
// summary is forwarded to the logger so restic's progress remains visible. The
// returned summary is the zero value when no summary line was seen. The reader
// is drained fully (a StdoutPipe constraint) before the caller waits.
func (r *Runner) parseBackupStream(out io.Reader) *BackupSummary {
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	summary := &BackupSummary{}
	for scanner.Scan() {
		line := scanner.Bytes()

		var msg resticMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Not a JSON object we recognize; log it like any other output.
			r.logLine("backup", string(line))
			continue
		}

		if msg.MessageType == "summary" {
			_ = json.Unmarshal(line, summary)
			continue
		}

		r.logLine("backup", string(line))
	}

	return summary
}

// logLine forwards a single line of restic output to the logger when present.
func (r *Runner) logLine(command, line string) {
	if r.logger != nil {
		r.logger.Info("restic output", "command", command, "line", line)
	}
}
