package log

// Log handles persistent storage of entries to disk.
// This is the source of truth — survives crashes.
//
// You will implement:
//   - Append(entry) → index
//   - Get(index) → entry
//   - LastIndex() → index
//   - Replay() → iterate all entries
