package kv

// Store is the state machine — an in-memory key-value map.
// Rebuilt from log on startup.
//
// You will implement:
//   - Apply(command) → result
//   - Get(key) → value, exists
//   - This must be deterministic — same commands = same state
