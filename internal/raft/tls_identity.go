package raft

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
)

// claimedRPCSender returns the Raft member id asserted by the RPC payload.
func claimedRPCSender(msg RPCMessage) string {
	switch msg.Type {
	case "RequestVote":
		var args RequestVoteArgs
		if json.Unmarshal(msg.Data, &args) != nil {
			return ""
		}
		return args.CandidateID
	case "AppendEntries":
		var args AppendEntriesArgs
		if json.Unmarshal(msg.Data, &args) != nil {
			return ""
		}
		return args.LeaderID
	case "InstallSnapshot":
		var args InstallSnapshotArgs
		if json.Unmarshal(msg.Data, &args) != nil {
			return ""
		}
		return args.LeaderID
	default:
		return ""
	}
}

func clientLeafCert(conn net.Conn) *x509.Certificate {
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return nil
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	return certs[0]
}

// identityNames are acceptable DNS SAN / CN values for claimedID (ADR-009).
func identityNames(claimedID string, idToAddr map[string]string) []string {
	if claimedID == "" {
		return nil
	}
	names := []string{claimedID}
	if addr, ok := idToAddr[claimedID]; ok && addr != "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		if host != "" && host != claimedID {
			names = append(names, host)
		}
	}
	return names
}

func certMatchesIdentity(cert *x509.Certificate, claimedID string, idToAddr map[string]string) bool {
	if cert == nil || claimedID == "" {
		return false
	}
	allowed := identityNames(claimedID, idToAddr)
	for _, dns := range cert.DNSNames {
		for _, want := range allowed {
			if dns == want {
				return true
			}
		}
	}
	// CN only when no DNS SANs (unusual); ADR prefers DNS SAN verification.
	if len(cert.DNSNames) == 0 && cert.Subject.CommonName != "" {
		for _, want := range allowed {
			if cert.Subject.CommonName == want {
				return true
			}
		}
	}
	return false
}
