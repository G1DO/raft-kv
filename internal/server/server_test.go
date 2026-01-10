package server

import (
	"bufio"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Helper: send command and get response
func sendCommand(conn net.Conn, cmd string) (string, error) {
	_, err := conn.Write([]byte(cmd + "\n"))
	if err != nil {
		return "", err
	}

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(response), nil
}

func TestServerPutAndGet(t *testing.T) {
	logPath := "/tmp/test_server_put_get.log"
	addr := "localhost:19001"
	defer os.Remove(logPath)
	os.Remove(logPath)

	srv, err := NewServer(addr, logPath)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	go srv.Start()
	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// PUT
	response, err := sendCommand(conn, "PUT foo bar")
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	if response != "OK" {
		t.Errorf("PUT: expected OK, got %s", response)
	}

	// GET
	response, err = sendCommand(conn, "GET foo")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if response != "bar" {
		t.Errorf("GET: expected bar, got %s", response)
	}
}

func TestServerDelete(t *testing.T) {
	logPath := "/tmp/test_server_delete.log"
	addr := "localhost:19002"
	defer os.Remove(logPath)
	os.Remove(logPath)

	srv, err := NewServer(addr, logPath)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	go srv.Start()
	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	sendCommand(conn, "PUT foo bar")
	response, _ := sendCommand(conn, "DELETE foo")
	if response != "OK" {
		t.Errorf("DELETE: expected OK, got %s", response)
	}

	response, _ = sendCommand(conn, "GET foo")
	if response != "NOT_FOUND" {
		t.Errorf("after DELETE: expected NOT_FOUND, got %s", response)
	}
}

func TestServerPersistence(t *testing.T) {
	logPath := "/tmp/test_server_persist.log"
	addr1 := "localhost:19003"
	addr2 := "localhost:19004"
	defer os.Remove(logPath)
	os.Remove(logPath)

	// First server: write data
	srv1, err := NewServer(addr1, logPath)
	if err != nil {
		t.Fatalf("NewServer 1 failed: %v", err)
	}

	go srv1.Start()
	time.Sleep(100 * time.Millisecond)

	conn1, _ := net.Dial("tcp", addr1)
	sendCommand(conn1, "PUT persist test123")
	conn1.Close()

	time.Sleep(100 * time.Millisecond)

	// Second server: should recover from log
	srv2, err := NewServer(addr2, logPath)
	if err != nil {
		t.Fatalf("NewServer 2 failed: %v", err)
	}

	go srv2.Start()
	time.Sleep(100 * time.Millisecond)

	conn2, _ := net.Dial("tcp", addr2)
	defer conn2.Close()
	response, _ := sendCommand(conn2, "GET persist")

	if response != "test123" {
		t.Errorf("after recovery: expected test123, got %s", response)
	}
}