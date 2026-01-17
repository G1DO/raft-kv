package kv

import (
	"encoding/json"
	"testing"
)

func TestPutAndGet(t *testing.T) {
	store := NewKVStore()

	// PUT a value
	result := store.Apply([]byte("PUT foo bar"))
	if result != "OK" {
		t.Errorf("PUT: expected OK, got %s", result)
	}

	// GET it back
	result = store.Apply([]byte("GET foo"))
	if result != "bar" {
		t.Errorf("GET: expected bar, got %s", result)
	}
}

func TestGetNotFound(t *testing.T) {
	store := NewKVStore()

	// GET non-existent key
	result := store.Apply([]byte("GET nonexistent"))
	if result != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %s", result)
	}
}

func TestDelete(t *testing.T) {
	store := NewKVStore()

	// PUT then DELETE
	store.Apply([]byte("PUT foo bar"))
	result := store.Apply([]byte("DELETE foo"))
	if result != "OK" {
		t.Errorf("DELETE: expected OK, got %s", result)
	}

	// GET should return NOT_FOUND
	result = store.Apply([]byte("GET foo"))
	if result != "NOT_FOUND" {
		t.Errorf("after DELETE: expected NOT_FOUND, got %s", result)
	}
}

func TestMultipleKeys(t *testing.T) {
	store := NewKVStore()

	// Store multiple keys
	store.Apply([]byte("PUT name alice"))
	store.Apply([]byte("PUT age 25"))
	store.Apply([]byte("PUT city tokyo"))

	// Get them back
	tests := []struct {
		key   string
		value string
	}{
		{"name", "alice"},
		{"age", "25"},
		{"city", "tokyo"},
	}

	for _, tt := range tests {
		result := store.Apply([]byte("GET " + tt.key))
		if result != tt.value {
			t.Errorf("GET %s: expected %s, got %s", tt.key, tt.value, result)
		}
	}
}

func TestOverwrite(t *testing.T) {
	store := NewKVStore()

	// PUT initial value
	store.Apply([]byte("PUT foo bar"))

	// Overwrite with new value
	store.Apply([]byte("PUT foo baz"))

	// GET should return new value
	result := store.Apply([]byte("GET foo"))
	if result != "baz" {
		t.Errorf("after overwrite: expected baz, got %s", result)
	}
}

func TestUnknownCommand(t *testing.T) {
	store := NewKVStore()

	result := store.Apply([]byte("INVALID foo bar"))
	if result != "ERROR: unknown command" {
		t.Errorf("expected error message, got %s", result)
	}
}

func TestDeterminism(t *testing.T) {
	// Same commands should produce same state in two stores
	store1 := NewKVStore()
	store2 := NewKVStore()

	commands := []string{
		"PUT foo bar",
		"PUT name alice",
		"DELETE foo",
		"PUT city tokyo",
	}

	// Apply same commands to both stores
	for _, cmd := range commands {
		store1.Apply([]byte(cmd))
		store2.Apply([]byte(cmd))
	}

	// Both should have same state
	tests := []string{"foo", "name", "city"}
	for _, key := range tests {
		result1 := store1.Apply([]byte("GET " + key))
		result2 := store2.Apply([]byte("GET " + key))
		if result1 != result2 {
			t.Errorf("determinism failed for %s: store1=%s, store2=%s", key, result1, result2)
		}
	}
}

// Helper to create a JSON command
func makeCommand(clientId string, requestId int64, op string) []byte {
	cmd := Command{
		ClientId:  clientId,
		RequestId: requestId,
		Op:        op,
	}
	data, _ := json.Marshal(cmd)
	return data
}

func TestDuplicateDetection_SameRequestReturnsCached(t *testing.T) {
	store := NewKVStore()

	// Request 1: PUT counter 1
	cmd1 := makeCommand("client-1", 1, "PUT counter 1")
	result1 := store.Apply(cmd1)
	if result1 != "OK" {
		t.Errorf("first PUT: expected OK, got %s", result1)
	}

	// Verify counter is 1
	if store.Apply([]byte("GET counter")) != "1" {
		t.Error("counter should be 1")
	}

	// RETRY request 1 (same requestId) - simulates network retry
	// This should return cached "OK" and NOT re-execute
	result1Again := store.Apply(cmd1)
	if result1Again != "OK" {
		t.Errorf("duplicate should return cached OK, got %s", result1Again)
	}

	// Counter should STILL be 1 (wasn't applied twice)
	if store.Apply([]byte("GET counter")) != "1" {
		t.Error("counter should still be 1 after duplicate")
	}

	// Now request 2: PUT counter 2
	cmd2 := makeCommand("client-1", 2, "PUT counter 2")
	store.Apply(cmd2)

	// Counter should now be 2
	if store.Apply([]byte("GET counter")) != "2" {
		t.Error("counter should be 2 after request 2")
	}

	// RETRY request 2 - should return cached "OK", not re-execute
	result2Again := store.Apply(cmd2)
	if result2Again != "OK" {
		t.Errorf("duplicate request 2 should return cached OK, got %s", result2Again)
	}
}

func TestDuplicateDetection_DifferentClientsDontInterfere(t *testing.T) {
	store := NewKVStore()

	// Client 1 sends request 1
	cmd1 := makeCommand("client-1", 1, "PUT key1 value1")
	store.Apply(cmd1)

	// Client 2 also sends request 1 (same requestId, different client)
	cmd2 := makeCommand("client-2", 1, "PUT key2 value2")
	result := store.Apply(cmd2)
	if result != "OK" {
		t.Errorf("different client same requestId should work, got %s", result)
	}

	// Both keys should exist
	get1 := makeCommand("client-1", 2, "GET key1")
	if store.Apply(get1) != "value1" {
		t.Error("key1 should be value1")
	}

	get2 := makeCommand("client-2", 2, "GET key2")
	if store.Apply(get2) != "value2" {
		t.Error("key2 should be value2")
	}
}

func TestDuplicateDetection_OldRequestIgnored(t *testing.T) {
	store := NewKVStore()

	// Process request 5 first
	cmd5 := makeCommand("client-1", 5, "PUT foo bar5")
	store.Apply(cmd5)

	// Now request 3 arrives (old/delayed) - should be ignored
	cmd3 := makeCommand("client-1", 3, "PUT foo bar3")
	store.Apply(cmd3)

	// foo should still be bar5
	get := makeCommand("client-1", 6, "GET foo")
	result := store.Apply(get)
	if result != "bar5" {
		t.Errorf("old request should be ignored: expected bar5, got %s", result)
	}
}

func TestSnapshot_SaveAndRestore(t *testing.T) {
	// 1. Create a store with some data
	store1 := NewKVStore()
	store1.Apply([]byte("PUT name alice"))
	store1.Apply([]byte("PUT city tokyo"))
	store1.Apply([]byte("PUT age 25"))

	// 2. Take a snapshot
	snapshot, err := store1.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// 3. Create a new empty store and restore
	store2 := NewKVStore()
	err = store2.Restore(snapshot)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// 4. Verify all data is there
	tests := []struct {
		key   string
		value string
	}{
		{"name", "alice"},
		{"city", "tokyo"},
		{"age", "25"},
	}

	for _, tt := range tests {
		result := store2.Apply([]byte("GET " + tt.key))
		if result != tt.value {
			t.Errorf("GET %s: expected %s, got %s", tt.key, tt.value, result)
		}
	}
}

func TestSnapshot_PreservesDuplicateDetection(t *testing.T) {
	// 1. Create store and process a request with clientId
	store1 := NewKVStore()
	cmd := makeCommand("client-1", 5, "PUT counter 100")
	store1.Apply(cmd)

	// 2. Take snapshot
	snapshot, _ := store1.Snapshot()

	// 3. Restore to new store
	store2 := NewKVStore()
	store2.Restore(snapshot)

	// 4. The duplicate detection should still work!
	// Replaying request 5 should return cached result, not re-execute
	result := store2.Apply(cmd)
	if result != "OK" {
		t.Errorf("expected cached OK, got %s", result)
	}

	// 5. Old request (id 3) should still be ignored
	oldCmd := makeCommand("client-1", 3, "PUT counter 999")
	store2.Apply(oldCmd)

	// Counter should still be 100 (not 999)
	get := makeCommand("client-1", 6, "GET counter")
	if store2.Apply(get) != "100" {
		t.Error("counter should still be 100 after old request")
	}
}