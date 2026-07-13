package server

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/G1DO/raft-kv/internal/kv"
)

const (
	auditEventClientMutate     = "audit.client.mutate"
	auditEventClientMembership = "audit.client.membership"
	actorUnauthenticated       = "unauthenticated"
)

type auditOutcome string

const (
	auditAllow auditOutcome = "allow"
	auditDeny  auditOutcome = "deny"
	auditError auditOutcome = "error"
)

type clientAudit struct {
	Event     string
	Outcome   auditOutcome
	Node      string
	Term      int
	Remote    string
	Action    string
	RequestID string
	TraceID   string
}

func emitClientAudit(log *slog.Logger, a clientAudit) {
	if log == nil {
		return
	}
	attrs := []any{
		"audit", true,
		"event", a.Event,
		"outcome", string(a.Outcome),
		"node", a.Node,
		"remote", a.Remote,
		"actor", actorUnauthenticated,
		"action", a.Action,
	}
	if a.Term > 0 {
		attrs = append(attrs, "term", a.Term)
	}
	if a.RequestID != "" {
		attrs = append(attrs, "request_id", a.RequestID)
	}
	if a.TraceID != "" {
		attrs = append(attrs, "trace_id", a.TraceID)
	}
	log.Info("security_audit", attrs...)
}

func outcomeFromClientResult(result string) auditOutcome {
	switch {
	case result == "OK" || result == "NOT_FOUND":
		return auditAllow
	case strings.HasPrefix(result, "NOT_LEADER"):
		return auditDeny
	default:
		return auditError
	}
}

func auditEventForVerb(verb string) string {
	switch verb {
	case "PUT", "DELETE":
		return auditEventClientMutate
	case "ADD_SERVER", "REMOVE_SERVER":
		return auditEventClientMembership
	default:
		return ""
	}
}

func isSecurityRelevantVerb(verb string) bool {
	return auditEventForVerb(verb) != ""
}

func requestIDFromCommand(command string) string {
	var cmd kv.Command
	if err := json.Unmarshal([]byte(command), &cmd); err != nil {
		return ""
	}
	if cmd.RequestId == 0 {
		return ""
	}
	return strconv.FormatInt(cmd.RequestId, 10)
}
