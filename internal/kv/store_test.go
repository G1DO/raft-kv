package kv

import "testing"

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