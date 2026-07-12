package raft

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
		DNSNames:     []string{"localhost", name},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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
