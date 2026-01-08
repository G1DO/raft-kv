package log

import (
	"bytes"
	"os"
	"testing"
)

// Helper: create a temp log file, return path and cleanup function
func tempLog(t *testing.T) (string, func()) {
	path := "/tmp/test_log_" + t.Name() + ".dat"
	cleanup := func() {
		os.Remove(path)
	}
	// Clean up any leftover from previous failed test
	os.Remove(path)
	return path, cleanup
}

func TestAppendAndGet(t *testing.T) {
	path, cleanup := tempLog(t)
	defer cleanup()

	// Create log
	log, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	// Append entry
	entry := []byte("PUT foo bar")
	index, err := log.Append(entry)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if index != 0 {
		t.Errorf("expected index 0, got %d", index)
	}

	// Get it back
	got, err := log.Get(0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Compare
	if !bytes.Equal(got, entry) {
		t.Errorf("expected %q, got %q", entry, got)
	}
}

func TestMultipleEntries(t *testing.T) {
	path, cleanup := tempLog(t)
	defer cleanup()

	log, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	// Append multiple entries
	entries := [][]byte{
		[]byte("PUT foo bar"),
		[]byte("PUT name alice"),
		[]byte("DELETE foo"),
	}

	for i, entry := range entries {
		index, err := log.Append(entry)
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
		if index != i {
			t.Errorf("expected index %d, got %d", i, index)
		}
	}

	// Get each one back
	for i, expected := range entries {
		got, err := log.Get(i)
		if err != nil {
			t.Fatalf("Get %d failed: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("entry %d: expected %q, got %q", i, expected, got)
		}
	}
}

func TestLastIndex(t *testing.T) {
	path, cleanup := tempLog(t)
	defer cleanup()

	log, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	// Empty log
	if log.LastIndex() != -1 {
		t.Errorf("empty log: expected -1, got %d", log.LastIndex())
	}

	// Add entries
	log.Append([]byte("one"))
	if log.LastIndex() != 0 {
		t.Errorf("after 1 entry: expected 0, got %d", log.LastIndex())
	}

	log.Append([]byte("two"))
	if log.LastIndex() != 1 {
		t.Errorf("after 2 entries: expected 1, got %d", log.LastIndex())
	}
}

func TestReplay(t *testing.T) {
	path, cleanup := tempLog(t)
	defer cleanup()

	// Create log and add entries
	log1, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	entries := [][]byte{
		[]byte("PUT foo bar"),
		[]byte("PUT name alice"),
		[]byte("DELETE foo"),
	}

	for _, entry := range entries {
		log1.Append(entry)
	}

	// Simulate crash: create new log from same file
	log2, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog (recovery) failed: %v", err)
	}

	// Replay to rebuild
	recovered, err := log2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	// Check we got all entries back
	if len(recovered) != len(entries) {
		t.Fatalf("expected %d entries, got %d", len(entries), len(recovered))
	}

	for i, expected := range entries {
		if !bytes.Equal(recovered[i], expected) {
			t.Errorf("entry %d: expected %q, got %q", i, expected, recovered[i])
		}
	}

	// Check offsets were rebuilt (Get should work)
	for i, expected := range entries {
		got, err := log2.Get(i)
		if err != nil {
			t.Fatalf("Get %d after replay failed: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("Get %d after replay: expected %q, got %q", i, expected, got)
		}
	}
}

func TestGetOutOfRange(t *testing.T) {
	path, cleanup := tempLog(t)
	defer cleanup()

	log, _ := NewLog(path)
	log.Append([]byte("one"))

	// Try to get index that doesn't exist
	_, err := log.Get(5)
	if err == nil {
		t.Error("expected error for out of range index, got nil")
	}

	_, err = log.Get(-1)
	if err == nil {
		t.Error("expected error for negative index, got nil")
	}
}
