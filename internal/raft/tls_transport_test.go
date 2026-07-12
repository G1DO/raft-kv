package raft

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMTLS_RequestVoteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := filepath.Join(dir, "ca.crt")
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)
	aCert, aKey := mustGenLeaf(t, dir, "node-a", caDER, caKey)
	bCert, bKey := mustGenLeaf(t, dir, "node-b", caDER, caKey)

	dirA := t.TempDir()
	dirB := t.TempDir()
	nodeA := NewRaft("node-a", nil, nil, filepath.Join(dirA, "log"), filepath.Join(dirA, "state"), filepath.Join(dirA, "snap"), nil, nil)
	nodeB := NewRaft("node-b", nil, nil, filepath.Join(dirB, "log"), filepath.Join(dirB, "state"), filepath.Join(dirB, "snap"), nil, nil)
	defer nodeA.Stop()
	defer nodeB.Stop()

	if err := nodeA.SetTLSConfig(&TLSConfig{CertFile: aCert, KeyFile: aKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.SetTLSConfig(&TLSConfig{CertFile: bCert, KeyFile: bKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}

	if err := nodeA.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	args := &RequestVoteArgs{Term: 1, CandidateID: "node-b", LastLogIndex: 0, LastLogTerm: 0}
	var reply RequestVoteReply
	ok := nodeB.callRPC(nodeA.rpcAddr, "RequestVote", args, &reply)
	if !ok {
		t.Fatal("mTLS RequestVote RPC failed")
	}
	if !reply.VoteGranted {
		t.Fatal("expected vote granted over mTLS")
	}
}

func TestMTLS_PlaintextClientRejected(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := filepath.Join(dir, "ca.crt")
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)
	cert, key := mustGenLeaf(t, dir, "node-a", caDER, caKey)

	data := t.TempDir()
	srv := NewRaft("node-a", nil, nil, filepath.Join(data, "log"), filepath.Join(data, "state"), filepath.Join(data, "snap"), nil, nil)
	defer srv.Stop()
	if err := srv.SetTLSConfig(&TLSConfig{CertFile: cert, KeyFile: key, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := srv.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialTimeout("tcp", srv.rpcAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = conn.Write([]byte(`{"Type":"RequestVote","Data":{}}` + "\n"))
	buf := make([]byte, 64)
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("expected plaintext read to fail against mTLS listener")
	}
}

func TestMTLS_WrongPeerIdentityRejected(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := filepath.Join(dir, "ca.crt")
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)
	aCert, aKey := mustGenLeaf(t, dir, "node-a", caDER, caKey)
	bCert, bKey := mustGenLeaf(t, dir, "node-b", caDER, caKey)

	dirA := t.TempDir()
	dirB := t.TempDir()
	nodeA := NewRaft("node-a", nil, nil, filepath.Join(dirA, "log"), filepath.Join(dirA, "state"), filepath.Join(dirA, "snap"), nil, nil)
	nodeB := NewRaft("node-b", nil, nil, filepath.Join(dirB, "log"), filepath.Join(dirB, "state"), filepath.Join(dirB, "snap"), nil, nil)
	defer nodeA.Stop()
	defer nodeB.Stop()

	if err := nodeA.SetTLSConfig(&TLSConfig{CertFile: aCert, KeyFile: aKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.SetTLSConfig(&TLSConfig{CertFile: bCert, KeyFile: bKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	nodeA.SetPeerIdentities(map[string]string{"node-b": "127.0.0.1:0"})
	nodeB.SetPeerIdentities(map[string]string{"node-a": "127.0.0.1:0"})

	if err := nodeA.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	// CA-valid node-b cert claiming to be node-a must not mutate Raft state.
	args := &RequestVoteArgs{Term: 1, CandidateID: "node-a", LastLogIndex: 0, LastLogTerm: 0}
	var reply RequestVoteReply
	ok := nodeB.callRPC(nodeA.rpcAddr, "RequestVote", args, &reply)
	if ok {
		t.Fatal("expected RPC to fail when cert identity mismatches claimed CandidateID")
	}
	nodeA.mu.Lock()
	term := nodeA.currentTerm
	voted := nodeA.votedFor
	nodeA.mu.Unlock()
	if term != 0 || voted != "" {
		t.Fatalf("Raft state changed after spoofed RPC: term=%d votedFor=%q", term, voted)
	}
}

func TestCertMatchesIdentity(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	certPath, _ := mustGenLeaf(t, dir, "raft-kv-0", caDER, caKey)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	idToAddr := map[string]string{"raft-kv-0": "raft-kv-0.raft-kv:9090"}
	if !certMatchesIdentity(cert, "raft-kv-0", idToAddr) {
		t.Fatal("expected match on DNS SAN == member id")
	}
	if certMatchesIdentity(cert, "raft-kv-1", idToAddr) {
		t.Fatal("expected mismatch for wrong member id")
	}
}

func TestMTLS_MissingClientCert(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := filepath.Join(dir, "ca.crt")
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)
	cert, key := mustGenLeaf(t, dir, "node-a", caDER, caKey)

	data := t.TempDir()
	srv := NewRaft("node-a", nil, nil, filepath.Join(data, "log"), filepath.Join(data, "state"), filepath.Join(data, "snap"), nil, nil)
	defer srv.Stop()
	if err := srv.SetTLSConfig(&TLSConfig{CertFile: cert, KeyFile: key, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := srv.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(mustRead(t, caPath)) {
		t.Fatal("ca pool")
	}
	raw, err := net.DialTimeout("tcp", srv.rpcAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(time.Second))
	// Trust the server CA but present no client certificate. Server handshake may
	// be lazy until the first Read (Go tls.Listener); require the connection fail.
	tconn := tls.Client(raw, &tls.Config{
		RootCAs:    pool,
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	})
	hsErr := tconn.Handshake()
	_ = tconn.SetDeadline(time.Now().Add(time.Second))
	_, _ = tconn.Write([]byte(`{"Type":"RequestVote","Data":{"Term":1,"CandidateID":"x"}}` + "\n"))
	_, readErr := tconn.Read(make([]byte, 64))
	if hsErr == nil && readErr == nil {
		t.Fatal("expected failure without client certificate")
	}
}

func TestMTLS_UnknownCA(t *testing.T) {
	dir := t.TempDir()
	ca1DER, ca1Key := mustGenCA(t)
	ca2DER, ca2Key := mustGenCA(t)
	ca1Path := filepath.Join(dir, "ca1.crt")
	ca2Path := filepath.Join(dir, "ca2.crt")
	mustWritePEM(t, ca1Path, "CERTIFICATE", ca1DER)
	mustWritePEM(t, ca2Path, "CERTIFICATE", ca2DER)
	aCert, aKey := mustGenLeaf(t, dir, "node-a", ca1DER, ca1Key)
	bCert, bKey := mustGenLeaf(t, dir, "node-b", ca2DER, ca2Key)

	dirA := t.TempDir()
	dirB := t.TempDir()
	nodeA := NewRaft("node-a", nil, nil, filepath.Join(dirA, "log"), filepath.Join(dirA, "state"), filepath.Join(dirA, "snap"), nil, nil)
	nodeB := NewRaft("node-b", nil, nil, filepath.Join(dirB, "log"), filepath.Join(dirB, "state"), filepath.Join(dirB, "snap"), nil, nil)
	defer nodeA.Stop()
	defer nodeB.Stop()

	if err := nodeA.SetTLSConfig(&TLSConfig{CertFile: aCert, KeyFile: aKey, CAFile: ca1Path}); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.SetTLSConfig(&TLSConfig{CertFile: bCert, KeyFile: bKey, CAFile: ca2Path}); err != nil {
		t.Fatal(err)
	}
	if err := nodeA.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	args := &RequestVoteArgs{Term: 1, CandidateID: "node-b"}
	var reply RequestVoteReply
	if nodeB.callRPC(nodeA.rpcAddr, "RequestVote", args, &reply) {
		t.Fatal("expected RPC failure across disjoint CAs")
	}
}

func TestMTLS_WrongServerName(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := filepath.Join(dir, "ca.crt")
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)
	// Server leaf has DNS SAN only — no 127.0.0.1 IP — so dialing by IP fails verify.
	srvCert, srvKey := mustGenLeafSANs(t, dir, "server", caDER, caKey, []string{"wrong.example"}, nil)
	cliCert, cliKey := mustGenLeaf(t, dir, "client", caDER, caKey)

	dirS := t.TempDir()
	dirC := t.TempDir()
	srv := NewRaft("server", nil, nil, filepath.Join(dirS, "log"), filepath.Join(dirS, "state"), filepath.Join(dirS, "snap"), nil, nil)
	cli := NewRaft("client", nil, nil, filepath.Join(dirC, "log"), filepath.Join(dirC, "state"), filepath.Join(dirC, "snap"), nil, nil)
	defer srv.Stop()
	defer cli.Stop()

	if err := srv.SetTLSConfig(&TLSConfig{CertFile: srvCert, KeyFile: srvKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := cli.SetTLSConfig(&TLSConfig{CertFile: cliCert, KeyFile: cliKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := srv.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	args := &RequestVoteArgs{Term: 1, CandidateID: "client"}
	var reply RequestVoteReply
	if cli.callRPC(srv.rpcAddr, "RequestVote", args, &reply) {
		t.Fatal("expected RPC failure when ServerName does not match server cert SAN")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustWritePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}

func mustGenCA(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "raft-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return caDER, caKey
}

func mustGenLeaf(t *testing.T, dir, name string, caDER []byte, caKey *ecdsa.PrivateKey) (certPath, keyPath string) {
	t.Helper()
	return mustGenLeafSANs(t, dir, name, caDER, caKey, []string{"localhost", name}, []net.IP{net.ParseIP("127.0.0.1")})
}

func mustGenLeafSANs(t *testing.T, dir, name string, caDER []byte, caKey *ecdsa.PrivateKey, dns []string, ips []net.IP) (certPath, keyPath string) {
	t.Helper()
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	mustWritePEM(t, certPath, "CERTIFICATE", leafDER)
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	mustWritePEM(t, keyPath, "EC PRIVATE KEY", keyDER)
	return certPath, keyPath
}
