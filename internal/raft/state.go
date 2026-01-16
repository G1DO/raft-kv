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

