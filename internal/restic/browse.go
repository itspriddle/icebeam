package restic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Snapshot mirrors one entry of `restic snapshots --json`. Only the fields
// icebeam renders or filters on are decoded; the rest of restic's snapshot
// object is ignored. Zero values stand in for absent fields.
type Snapshot struct {
	// ID is the full snapshot id.
	ID string `json:"id"`
	// ShortID is the abbreviated snapshot id restic prints by default.
	ShortID string `json:"short_id"`
	// Time is when the snapshot was created.
	Time time.Time `json:"time"`
	// Hostname is the host the snapshot was taken on.
	Hostname string `json:"hostname"`
	// Tags are the snapshot's tags.
	Tags []string `json:"tags"`
	// Paths are the paths the snapshot covers.
	Paths []string `json:"paths"`
}

// FindResult mirrors one entry of `restic find <pattern> --json`: the snapshot a
// match was found in and the matching nodes within it.
type FindResult struct {
	// Hits is the number of matches in the snapshot.
	Hits int `json:"hits"`
	// Snapshot is the id of the snapshot the matches were found in.
	Snapshot string `json:"snapshot"`
	// Matches are the matching files/directories within the snapshot.
	Matches []FindMatch `json:"matches"`
}

// FindMatch mirrors one match within a FindResult. Only the fields icebeam
// renders are decoded.
type FindMatch struct {
	// Path is the matching file's path within the snapshot.
	Path string `json:"path"`
	// Type is the node type (e.g. "file", "dir").
	Type string `json:"type"`
	// Size is the file size in bytes.
	Size uint64 `json:"size"`
	// MTime is the file's modification time.
	MTime time.Time `json:"mtime"`
}

// LSResult holds the parsed output of `restic ls <snapshot> --json`: the
// snapshot the listing came from and the nodes it contains.
type LSResult struct {
	// Snapshot is the snapshot whose contents were listed.
	Snapshot Snapshot
	// Nodes are the files and directories in the listing.
	Nodes []LSNode
}

// LSNode mirrors a "node" message of `restic ls --json`. Only the fields icebeam
// renders are decoded.
type LSNode struct {
	// Path is the node's path within the snapshot.
	Path string `json:"path"`
	// Type is the node type (e.g. "file", "dir").
	Type string `json:"type"`
	// Size is the file size in bytes (zero for directories).
	Size uint64 `json:"size"`
	// Mode is restic's permission string (e.g. "drwxr-xr-x").
	Mode string `json:"permissions"`
	// MTime is the node's modification time.
	MTime time.Time `json:"mtime"`
}

// Snapshots runs `restic snapshots` with --json and returns the parsed list. args
// is the argument vector after the subcommand (e.g. {"--tag", "home"}); --json is
// added by the runner. `snapshots --json` emits a single JSON array, so the
// standard JSON capture path applies.
func (r *Runner) Snapshots(ctx context.Context, args ...string) ([]Snapshot, error) {
	var snapshots []Snapshot
	full := append([]string{"snapshots"}, args...)
	if err := r.RunJSON(ctx, &snapshots, full...); err != nil {
		return nil, err
	}
	return snapshots, nil
}

// Find runs `restic find <pattern>` with --json and returns the parsed results.
// args is the argument vector after the subcommand (the pattern plus any filters,
// e.g. {"*.go", "--tag", "home"}); --json is added by the runner. `find --json`
// emits a single JSON array.
func (r *Runner) Find(ctx context.Context, args ...string) ([]FindResult, error) {
	var results []FindResult
	full := append([]string{"find"}, args...)
	if err := r.RunJSON(ctx, &results, full...); err != nil {
		return nil, err
	}
	return results, nil
}

// LS runs `restic ls <snapshot> [path]` with --json and returns the snapshot and
// its nodes. args is the argument vector after the subcommand (the snapshot
// selector, optional path, and any filters); --json is added here.
//
// Unlike `snapshots`/`find`, `ls --json` emits a stream of newline-delimited JSON
// messages: a leading "snapshot" message followed by one "node" message per
// entry. That can't be parsed as a single document, so it is streamed line by
// line like Backup.
func (r *Runner) LS(ctx context.Context, args ...string) (*LSResult, error) {
	lsArgs := append([]string{"ls", "--json"}, args...)

	cmd, err := r.command(ctx, lsArgs)
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
		return nil, fmt.Errorf("restic: start ls: %w", err)
	}

	// restic writes the JSON stream to stdout; drain stderr to the logger so
	// progress/errors stay visible without polluting the JSON parse.
	stderrDone := make(chan struct{})
	go func() {
		r.streamOutput(stderr, "ls")
		close(stderrDone)
	}()

	result := r.parseLSStream(stdout)
	<-stderrDone

	if err := r.wait(ctx, cmd, lsArgs); err != nil {
		return nil, err
	}
	return result, nil
}

// parseLSStream reads restic's newline-delimited ls JSON, capturing the leading
// "snapshot" message and every "node" message. Unrecognized lines are forwarded
// to the logger. The reader is drained fully (a StdoutPipe constraint) before the
// caller waits.
func (r *Runner) parseLSStream(out io.Reader) *LSResult {
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	result := &LSResult{}
	for scanner.Scan() {
		line := scanner.Bytes()

		var msg resticMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Not a JSON object we recognize; log it like any other output.
			r.logLine("ls", string(line))
			continue
		}

		switch msg.MessageType {
		case "snapshot":
			_ = json.Unmarshal(line, &result.Snapshot)
		case "node":
			var node LSNode
			if err := json.Unmarshal(line, &node); err == nil {
				result.Nodes = append(result.Nodes, node)
			}
		default:
			r.logLine("ls", string(line))
		}
	}

	return result
}
