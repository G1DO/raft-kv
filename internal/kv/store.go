package kv

import (
	"encoding/json"
	"strings"
)

// Command represents a client request with deduplication info
type Command struct {
	ClientId  string `json:"clientId"`  // unique client identifier
	RequestId int64  `json:"requestId"` // monotonically increasing per client
	Op        string `json:"op"`        // the actual command: "PUT foo bar"
}

type KVStore struct {
	data        map[string]string // the actual KV data
	lastRequest map[string]int64  // clientId → last requestId processed
	lastResult  map[string]string // clientId → result of that request
}

// NewKVStore creates a new KV store with initialized maps
func NewKVStore() *KVStore {
	return &KVStore{
		data:        make(map[string]string),
		lastRequest: make(map[string]int64),
		lastResult:  make(map[string]string),
	}
}

// Apply processes a command, handling duplicates
// Input is JSON-encoded Command struct
func (s *KVStore) Apply(commandBytes []byte) string {
	// 1. Parse the command
	var cmd Command
	if err := json.Unmarshal(commandBytes, &cmd); err != nil {
		// Fallback: treat as raw command string (backward compatibility)
		return s.executeOp(string(commandBytes))
	}

	// 2. Check for duplicate: have we seen this request before?
	if lastReq, exists := s.lastRequest[cmd.ClientId]; exists {
		if cmd.RequestId <= lastReq {
			// Duplicate! Return cached result
			return s.lastResult[cmd.ClientId]
		}
	}

	// 3. Not a duplicate - execute the command
	result := s.executeOp(cmd.Op)

	// 4. Remember this request for future duplicate detection
	s.lastRequest[cmd.ClientId] = cmd.RequestId
	s.lastResult[cmd.ClientId] = result

	return result
}

// executeOp runs the actual PUT/GET/DELETE operation
func (s *KVStore) executeOp(op string) string {
	parts := strings.Split(op, " ")
	if len(parts) == 0 {
		return "ERROR: empty command"
	}

	switch parts[0] {
	case "PUT":
		if len(parts) < 3 {
			return "ERROR: PUT requires key and value"
		}
		key := parts[1]
		value := parts[2]
		s.data[key] = value
		return "OK"
	case "GET":
		if len(parts) < 2 {
			return "ERROR: GET requires key"
		}
		key := parts[1]
		value, exists := s.data[key]
		if exists {
			return value
		}
		return "NOT_FOUND"
	case "DELETE":
		if len(parts) < 2 {
			return "ERROR: DELETE requires key"
		}
		delete(s.data, parts[1])
		return "OK"
	default:
		return "ERROR: unknown command"
	}
}


// Store is the state machine — an in-memory key-value map.
// Rebuilt from log on startup.
//
// You will implement:
//   - Apply(command) → result
//   - Get(key) → value, exists
//   - This must be deterministic — same commands = same state
