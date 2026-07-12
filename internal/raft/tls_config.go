package raft

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLSConfig holds filesystem paths for peer mTLS material (ADR-009 / ADR-010).
// Nil or all-empty paths means plaintext peer RPC (tests / explicit unset only).
type TLSConfig struct {
	CertFile string // leaf certificate PEM (tls.crt)
	KeyFile  string // leaf private key PEM (tls.key)
	CAFile   string // CA bundle PEM (ca.crt) — trust store for dial and accept
}

// Enabled reports whether TLS paths were requested (any field non-empty).
func (c *TLSConfig) Enabled() bool {
	if c == nil {
		return false
	}
	return c.CertFile != "" || c.KeyFile != "" || c.CAFile != ""
}

// Validate enforces ADR-010 fail-closed rules: all three paths together, and
// each file must exist. Empty config (TLS off) is valid.
func (c *TLSConfig) Validate() error {
	if c == nil || !c.Enabled() {
		return nil
	}
	if c.CertFile == "" || c.KeyFile == "" || c.CAFile == "" {
		return fmt.Errorf("raft TLS: cert, key, and CA paths are all required when any is set (got cert=%q key=%q ca=%q)",
			c.CertFile, c.KeyFile, c.CAFile)
	}
	for _, p := range []struct {
		name, path string
	}{
		{"cert", c.CertFile},
		{"key", c.KeyFile},
		{"CA", c.CAFile},
	} {
		if _, err := os.Stat(p.path); err != nil {
			return fmt.Errorf("raft TLS: %s file %q: %w", p.name, p.path, err)
		}
	}
	return nil
}

// mtlsState is loaded PEM material for peer dial/listen. Election logic never
// sees this — only StartRPCServer / callRPC.
type mtlsState struct {
	cert tls.Certificate
	pool *x509.CertPool
}

func loadMTLS(cfg *TLSConfig) (*mtlsState, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("raft TLS: load cert/key: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("raft TLS: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("raft TLS: no certificates parsed from CA file %q", cfg.CAFile)
	}
	return &mtlsState{cert: cert, pool: pool}, nil
}

func (m *mtlsState) serverTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    m.pool,
		MinVersion:   tls.VersionTLS12,
	}
}

func (m *mtlsState) clientTLSConfig(serverName string) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.cert},
		RootCAs:      m.pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}
}
