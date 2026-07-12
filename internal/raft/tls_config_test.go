package raft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTLSConfig_Validate_emptyOK(t *testing.T) {
	if err := (*TLSConfig)(nil).Validate(); err != nil {
		t.Fatalf("nil: %v", err)
	}
	if err := (&TLSConfig{}).Validate(); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if (&TLSConfig{}).Enabled() {
		t.Fatal("empty should not be Enabled")
	}
}

func TestTLSConfig_Validate_partialRejected(t *testing.T) {
	cfg := &TLSConfig{CertFile: "/tmp/only-cert.pem"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for partial config")
	}
	if !strings.Contains(err.Error(), "all required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled() {
		t.Fatal("partial should still be Enabled")
	}
}

func TestTLSConfig_Validate_missingFile(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "tls.crt")
	key := filepath.Join(dir, "tls.key")
	ca := filepath.Join(dir, "ca.crt")
	mustWrite(t, cert, "cert")
	mustWrite(t, key, "key")
	// ca missing
	cfg := &TLSConfig{CertFile: cert, KeyFile: key, CAFile: ca}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
	if !strings.Contains(err.Error(), "CA file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTLSConfig_Validate_allPresent(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "tls.crt")
	key := filepath.Join(dir, "tls.key")
	ca := filepath.Join(dir, "ca.crt")
	mustWrite(t, cert, "cert")
	mustWrite(t, key, "key")
	mustWrite(t, ca, "ca")
	cfg := &TLSConfig{CertFile: cert, KeyFile: key, CAFile: ca}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled() {
		t.Fatal("expected Enabled")
	}
}

func TestRaft_SetTLSConfig(t *testing.T) {
	dir := t.TempDir()
	r := NewRaft("n1", nil, nil, filepath.Join(dir, "log"), filepath.Join(dir, "state"), filepath.Join(dir, "snap"), nil, nil)
	defer r.Stop()

	if r.TLSEnabled() {
		t.Fatal("expected TLS off by default")
	}
	if err := r.SetTLSConfig(&TLSConfig{CertFile: "only"}); err == nil {
		t.Fatal("expected partial config error")
	}
	if r.TLSEnabled() {
		t.Fatal("partial must not enable TLS")
	}

	cert := filepath.Join(dir, "tls.crt")
	key := filepath.Join(dir, "tls.key")
	ca := filepath.Join(dir, "ca.crt")
	caDER, caKey := mustGenCA(t)
	mustWritePEM(t, ca, "CERTIFICATE", caDER)
	cert, key = mustGenLeaf(t, dir, "n1", caDER, caKey)
	if err := r.SetTLSConfig(&TLSConfig{CertFile: cert, KeyFile: key, CAFile: ca}); err != nil {
		t.Fatal(err)
	}
	if !r.TLSEnabled() {
		t.Fatal("expected TLS on")
	}
	if err := r.SetTLSConfig(nil); err != nil {
		t.Fatal(err)
	}
	if r.TLSEnabled() {
		t.Fatal("expected TLS off after nil")
	}
}

// TestTLS_NoInProcessReload documents Phase B #9: PEM files are read once in
// SetTLSConfig; overwriting paths on disk does not update r.mtls until restart.
func TestTLS_NoInProcessReload(t *testing.T) {
	dir := t.TempDir()
	r := NewRaft("n1", nil, nil, filepath.Join(dir, "log"), filepath.Join(dir, "state"), filepath.Join(dir, "snap"), nil, nil)
	defer r.Stop()

	caPath := filepath.Join(dir, "ca.crt")
	caDER, caKey := mustGenCA(t)
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)

	certPath, keyPath := mustGenLeaf(t, dir, "leaf", caDER, caKey)
	if err := r.SetTLSConfig(&TLSConfig{CertFile: certPath, KeyFile: keyPath, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}

	r.mu.Lock()
	first := append([]byte(nil), r.mtls.cert.Certificate[0]...)
	r.mu.Unlock()

	renewCert, renewKey := mustGenLeaf(t, dir, "leaf-renewed", caDER, caKey)
	renewDER, err := os.ReadFile(renewCert)
	if err != nil {
		t.Fatal(err)
	}
	renewKeyPEM, err := os.ReadFile(renewKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, renewDER, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, renewKeyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	r.mu.Lock()
	still := r.mtls.cert.Certificate[0]
	r.mu.Unlock()
	if string(still) == string(renewDER) {
		t.Fatal("expected in-memory cert unchanged after on-disk overwrite without SetTLSConfig")
	}
	if string(still) != string(first) {
		t.Fatal("in-memory cert should remain the originally loaded leaf")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
