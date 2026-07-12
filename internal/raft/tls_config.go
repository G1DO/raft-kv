package raft

import (
	"fmt"
	"os"
)

// TLSConfig holds filesystem paths for peer mTLS material (ADR-009 / ADR-010).
// Nil or all-empty paths means plaintext peer RPC (tests / explicit unset only).
// Dial and listen still use these paths in Phase A #2; this type is the contract
// at the server/Raft boundary so election logic never sees raw tls.Config.
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
