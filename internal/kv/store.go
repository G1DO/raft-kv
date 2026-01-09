package kv
import (
	"strings"
)
type KVStore struct {
    data map[string]string
}
//it create new struct and return pointer on it
func NewKVStore() *KVStore {
    return &KVStore{
        data: make(map[string]string),
    }
}
func (s *KVStore) Apply(command []byte) string {
    // Convert to string
    cmd := string(command)
    
    // Split by spaces: "PUT foo bar" → ["PUT", "foo", "bar"]
    parts := strings.Split(cmd, " ")
        // What type of command?
    switch parts[0] {
    case "PUT":
        // handle PUT
        key:= parts[1]
        value := parts[2]
        s.data[key]=value
        return "OK"
    case "GET":
        // handle GET
        key := parts[1]
        value, exists := s.data[key]
        if exists {
            return value
        }
        return "NOT_FOUND"
    case "DELETE":
        // handle DELETE
       delete(s.data,parts[1])
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
