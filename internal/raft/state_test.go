package raft

import (
    "os"
    "path/filepath"
    "testing"
)

func TestSaveAndLoadRaftState(t *testing.T) {
    // 1. Create a temp directory for test files
    // 2. Save a state with CurrentTerm=5, VotedFor="node-1"
    // 3. Load it back
    // 4. Check if values match
}

func TestLoadRaftState_FileNotExist(t *testing.T) {
    // 1. Try to load from a path that doesn't exist
    // 2. Should return empty state (term=0, votedFor="")
    // 3. Should return nil error
}

func TestLoadRaftState_CorruptedJSON(t *testing.T) {
    // 1. Create a temp file with invalid JSON like "{broken"
    // 2. Try to load it
    // 3. Should return an error
}
