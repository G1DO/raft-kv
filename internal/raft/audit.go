package raft

const auditEventPeerTLSFail = "audit.peer.tls_fail"

func (r *Raft) emitPeerTLSAudit(remote, action, outcome string) {
	if r.logger == nil {
		return
	}
	attrs := []any{
		"audit", true,
		"event", auditEventPeerTLSFail,
		"outcome", outcome,
		"node", r.id,
		"remote", remote,
		"action", action,
	}
	if term := r.CurrentTerm(); term > 0 {
		attrs = append(attrs, "term", term)
	}
	r.logger.Info("security_audit", attrs...)
}
