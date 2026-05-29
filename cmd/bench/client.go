package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
)

// Client holds a persistent TCP connection to one raft-kv node and speaks the
// line-based text protocol defined in internal/server/raft_server.go.
type Client struct {
	addr string
	conn net.Conn
	rw   *bufio.ReadWriter
}

// do dials addr, sends one command (following at most one NOT_LEADER redirect),
// and closes the connection. Convenience for one-shot probes.
func do(addr, command string) (string, error) {
	cl, err := Dial(addr)
	if err != nil {
		return "", err
	}
	defer cl.Close()
	return cl.Do(command)
}

// Dial opens a TCP connection to addr.
func Dial(addr string) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Client{
		addr: addr,
		conn: conn,
		rw:   newRW(conn),
	}, nil
}

// Do sends command and returns the trimmed response, following at most one
// NOT_LEADER <addr> redirect. If the server replies NOT_LEADER with no hint,
// the response is returned as-is for the caller to handle.
func (c *Client) Do(command string) (string, error) {
	resp, err := c.roundtrip(command)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "NOT_LEADER ") {
		hint := strings.TrimPrefix(resp, "NOT_LEADER ")
		if rerr := c.reconnect(hint); rerr != nil {
			// hint address unreachable — return original response
			return resp, nil
		}
		return c.roundtrip(command)
	}
	return resp, nil
}

// Addr returns the address this client is currently connected to.
func (c *Client) Addr() string { return c.addr }

// Close closes the underlying TCP connection.
func (c *Client) Close() error { return c.conn.Close() }

// roundtrip sends one command line and reads one response line.
func (c *Client) roundtrip(command string) (string, error) {
	if _, err := fmt.Fprintf(c.rw, "%s\n", command); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if err := c.rw.Flush(); err != nil {
		return "", fmt.Errorf("flush: %w", err)
	}
	line, err := c.rw.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// reconnect closes the current connection and opens a new one to newAddr.
func (c *Client) reconnect(newAddr string) error {
	c.conn.Close()
	conn, err := net.Dial("tcp", newAddr)
	if err != nil {
		return err
	}
	c.addr = newAddr
	c.conn = conn
	c.rw = newRW(conn)
	return nil
}

func newRW(conn net.Conn) *bufio.ReadWriter {
	return bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
}
