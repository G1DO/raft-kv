package server

// Server handles client connections and the protocol.
// Receives commands, writes to log, applies to state machine, responds.
//
// You will implement:
//   - Listen for TCP connections
//   - Parse commands: PUT key value, GET key, DELETE key
//   - Coordinate log + state machine
//   - Return responses to clients
