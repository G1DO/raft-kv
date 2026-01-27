package raft

import (
	"os"
	"encoding/json"
	"fmt"
)

// log for vote and current term not with log.go cuz it will replaced over and overwrite
type RaftState struct {
    CurrentTerm int
    VotedFor    string
}

// SnapshotState holds snapshot data for persistence
// Saved separately from RaftState because snapshots are large and written less frequently
type SnapshotState struct {
    LastIncludedIndex int    // snapshot covers entries 1 through this index
    LastIncludedTerm  int    // term of the entry at LastIncludedIndex
    Data              []byte // serialized state machine
}

func SaveRaftState(path string, state RaftState) error {
    data, err := json.Marshal(state)
    if err != nil {
        return fmt.Errorf("failed to marshal state: %w", err)
    }

    err = os.WriteFile(path, data, 0644)
    if err != nil {
        return fmt.Errorf("failed to write state file: %w", err)
    }

    return nil
}

func LoadRaftState(path string) (RaftState, error) {
    // 1. Try to read file FIRST
    data, err := os.ReadFile(path)

    // 2. Check if file doesn't exist (that's OK - fresh start)
    if os.IsNotExist(err) {
        return RaftState{}, nil
    }

    // 3. Check for OTHER errors (that's NOT OK)
    if err != nil {
        return RaftState{}, fmt.Errorf("failed to read state: %w", err)
    }

    // 4. Unmarshal (use = not := since err already exists)
    var state RaftState
    err = json.Unmarshal(data, &state)
    if err != nil {
        return RaftState{}, fmt.Errorf("failed to unmarshal state: %w", err)
    }

    // 5. Return success
    return state, nil
}

// SaveSnapshot persists snapshot to disk
// Uses atomic write pattern: write to temp file, then rename
// This prevents corruption if crash happens mid-write
func SaveSnapshot(path string, snapshot SnapshotState) error {
    data, err := json.Marshal(snapshot)
    if err != nil {
        return fmt.Errorf("failed to marshal snapshot: %w", err)
    }

    // Write to temp file first, then rename (atomic on most filesystems)
    tempPath := path + ".tmp"
    err = os.WriteFile(tempPath, data, 0644)
    if err != nil {
        return fmt.Errorf("failed to write snapshot temp file: %w", err)
    }

    // Rename is atomic - either old file or new file, never corrupted partial
    err = os.Rename(tempPath, path)
    if err != nil {
        return fmt.Errorf("failed to rename snapshot file: %w", err)
    }

    return nil
}

// LoadSnapshot reads snapshot from disk
// Returns empty SnapshotState if file doesn't exist (fresh start)
func LoadSnapshot(path string) (SnapshotState, error) {
    data, err := os.ReadFile(path)

    // File doesn't exist = no snapshot yet (fresh start)
    if os.IsNotExist(err) {
        return SnapshotState{}, nil
    }

    if err != nil {
        return SnapshotState{}, fmt.Errorf("failed to read snapshot: %w", err)
    }

    var snapshot SnapshotState
    err = json.Unmarshal(data, &snapshot)
    if err != nil {
        return SnapshotState{}, fmt.Errorf("failed to unmarshal snapshot: %w", err)
    }

    return snapshot, nil
}
