package main

import (
	"fmt"
	"os"

	"github.com/G1DO/raft-kv/internal/server"
)

func main() {
	addr := "localhost:8080"
	logPath := "data/raft.log"

	// Create data directory if it doesn't exist
	os.MkdirAll("data", 0755)

	fmt.Println("Starting raft-kv server...")

	srv, err := server.NewServer(addr, logPath)
	if err != nil {
		fmt.Printf("Failed to create server: %v\n", err)
		os.Exit(1)
	}

	err = srv.Start()
	if err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}