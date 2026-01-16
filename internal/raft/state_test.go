package raft

import (
    "os"
    "path/filepath"
    "testing"
)

func TestSaveAndLoadRaftState(t *testing.T) {
    // 1. Create a temp directory for test files
    tmpDir, err := os.MkdirTemp("", "raft-state-test")
    if err != nil {
        t.Fatalf("failed to create temp dir: %v", err)
    }
    t.Cleanup(func() { os.RemoveAll(tmpDir) })

    statePath := filepath.Join(tmpDir, "state.json")

    // 2. Save a state with CurrentTerm=5, VotedFor="node-1"
    original := RaftState{
        CurrentTerm: 5,
        VotedFor:    "node-1",
    }
    err = SaveRaftState(statePath, original)
    if err != nil {
        t.Fatalf("SaveRaftState failed: %v", err)
    }

    // 3. Load it back
    loaded, err := LoadRaftState(statePath)
    if err != nil {
        t.Fatalf("LoadRaftState failed: %v", err)
    }

    // 4. Check if values match
    if loaded.CurrentTerm != original.CurrentTerm {
        t.Errorf("CurrentTerm mismatch: got %d, want %d", loaded.CurrentTerm, original.CurrentTerm)
    }
    if loaded.VotedFor != original.VotedFor {
        t.Errorf("VotedFor mismatch: got %s, want %s", loaded.VotedFor, original.VotedFor)
    }
}

func TestLoadRaftState_FileNotExist(t *testing.T) {
    // 1. Try to load from a path that doesn't exist
    state, err := LoadRaftState("/nonexistent/path/state.json")

    // 2. Should return nil error (fresh start is OK)
    if err != nil {
        t.Errorf("expected nil error for non-existent file, got: %v", err)
    }

    // 3. Should return empty state (term=0, votedFor="")
    if state.CurrentTerm != 0 {
        t.Errorf("expected CurrentTerm=0, got %d", state.CurrentTerm)
    }
    if state.VotedFor != "" {
        t.Errorf("expected VotedFor=\"\", got %s", state.VotedFor)
    }
}

func TestLoadRaftState_CorruptedJSON(t *testing.T) {
    // 1. Create a temp file with invalid JSON like "{broken"
    tmpDir, err := os.MkdirTemp("", "raft-state-test")
    if err != nil {
        t.Fatalf("failed to create temp dir: %v", err)
    }
    t.Cleanup(func() { os.RemoveAll(tmpDir) })

    statePath := filepath.Join(tmpDir, "state.json")
    err = os.WriteFile(statePath, []byte("{broken"), 0644)
    if err != nil {
        t.Fatalf("failed to write corrupted file: %v", err)
    }

    // 2. Try to load it
    _, err = LoadRaftState(statePath)

    // 3. Should return an error
    if err == nil {
        t.Error("expected error for corrupted JSON, got nil")
    }
}
