package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// nodeSpec describes one cluster node's identity and listen addresses.
type nodeSpec struct {
	ID         string
	ClientAddr string
	RaftAddr   string
}

// defaultNodes is the standard 3-node layout, matching scripts/cluster-up.sh.
var defaultNodes = []nodeSpec{
	{"node1", "localhost:8081", "localhost:9001"},
	{"node2", "localhost:8082", "localhost:9002"},
	{"node3", "localhost:8083", "localhost:9003"},
}

// Cluster manages a set of raft-kv nodes running as child processes.
type Cluster struct {
	binary  string // path to the raft-kv binary
	dataDir string // root data directory; each node gets dataDir/<id>
	nodes   []nodeSpec
	procs   map[string]*os.Process // running node id -> process
}

// BuildBinary compiles the raft-kv server binary from srcRoot into outPath.
// srcRoot should be the repository root (contains cmd/server).
func BuildBinary(srcRoot, outPath string) error {
	cmd := exec.Command("go", "build", "-o", outPath, "./cmd/server")
	cmd.Dir = srcRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// setup builds the server binary into dataDir, creates a fresh 3-node cluster
// (wiping any prior per-node data), and starts all nodes. Callers then WaitLeader.
// src is the repo root to build ./cmd/server from.
func setup(dataDir, src string) (*Cluster, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	binary := filepath.Join(dataDir, "raft-kv")
	if err := BuildBinary(src, binary); err != nil {
		return nil, fmt.Errorf("build binary: %w", err)
	}
	c := NewCluster(binary, dataDir, defaultNodes)
	if err := c.CleanData(); err != nil {
		return nil, fmt.Errorf("clean data: %w", err)
	}
	if err := c.StartAll(); err != nil {
		return nil, fmt.Errorf("start cluster: %w", err)
	}
	return c, nil
}

// NewCluster creates a Cluster. Call StartAll to launch nodes.
func NewCluster(binary, dataDir string, nodes []nodeSpec) *Cluster {
	return &Cluster{
		binary:  binary,
		dataDir: dataDir,
		nodes:   nodes,
		procs:   make(map[string]*os.Process),
	}
}

// StartAll launches every node in the cluster.
func (c *Cluster) StartAll() error {
	for _, n := range c.nodes {
		if err := c.Start(n.ID); err != nil {
			return fmt.Errorf("start %s: %w", n.ID, err)
		}
	}
	return nil
}

// Start launches the named node as a child process.
func (c *Cluster) Start(id string) error {
	n, ok := c.find(id)
	if !ok {
		return fmt.Errorf("unknown node %q", id)
	}
	nodeData := filepath.Join(c.dataDir, id)
	if err := os.MkdirAll(nodeData, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", nodeData, err)
	}

	cmd := exec.Command(c.binary,
		"--id", n.ID,
		"--addr", n.ClientAddr,
		"--raft-addr", n.RaftAddr,
		"--peers", c.peersArg(id),
		"--data", nodeData,
	)
	// Forward node output to stderr so bench stdout stays clean for measurements.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec %s: %w", id, err)
	}
	c.procs[id] = cmd.Process
	return nil
}

// Kill hard-kills the named node with SIGKILL, simulating a crash.
func (c *Cluster) Kill(id string) error {
	p, ok := c.procs[id]
	if !ok {
		return fmt.Errorf("node %q is not running", id)
	}
	err := p.Kill()
	delete(c.procs, id)
	return err
}

// KillAll hard-kills every running node.
func (c *Cluster) KillAll() {
	for id, p := range c.procs {
		p.Kill()
		delete(c.procs, id)
	}
}

// WaitLeader polls running nodes until one accepts a write, then returns its
// client address. It mirrors the patience of TestCluster_ElectsLeader
// (raft_test.go:434) but uses active probing instead of a fixed sleep.
func (c *Cluster) WaitLeader(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, n := range c.nodes {
			if _, running := c.procs[n.ID]; !running {
				continue
			}
			cl, err := Dial(n.ClientAddr)
			if err != nil {
				continue
			}
			resp, err := cl.Do("PUT __bench_probe__ 1")
			cl.Close()
			if err == nil && resp == "OK" {
				return n.ClientAddr, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", fmt.Errorf("no leader elected within %v", timeout)
}

// CleanData removes all per-node data directories so nodes start from scratch.
func (c *Cluster) CleanData() error {
	for _, n := range c.nodes {
		if err := os.RemoveAll(filepath.Join(c.dataDir, n.ID)); err != nil {
			return err
		}
	}
	return nil
}

// find returns the nodeSpec for id.
func (c *Cluster) find(id string) (nodeSpec, bool) {
	for _, n := range c.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return nodeSpec{}, false
}

// idForClientAddr maps a client listen address back to its node id, e.g. the
// address WaitLeader returns -> "node1".
func (c *Cluster) idForClientAddr(addr string) (string, bool) {
	for _, n := range c.nodes {
		if n.ClientAddr == addr {
			return n.ID, true
		}
	}
	return "", false
}

// WaitNodeUp blocks until the node at clientAddr is serving the client API
// (a connection succeeds and any protocol reply comes back — OK or NOT_LEADER),
// which is the signal that a freshly restarted node has rejoined and is ready.
func (c *Cluster) WaitNodeUp(clientAddr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cl, err := Dial(clientAddr)
		if err == nil {
			resp, derr := cl.roundtrip("GET __bench_probe__")
			cl.Close()
			if derr == nil && resp != "" {
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("node %s not up within %v", clientAddr, timeout)
}

// peersArg builds the --peers value for node id: every other node formatted as
// id@raftAddr@clientAddr, comma-separated — same as scripts/cluster-up.sh.
func (c *Cluster) peersArg(id string) string {
	var parts []string
	for _, n := range c.nodes {
		if n.ID == id {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s@%s@%s", n.ID, n.RaftAddr, n.ClientAddr))
	}
	return strings.Join(parts, ",")
}
