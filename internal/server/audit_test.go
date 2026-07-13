package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestOutcomeFromClientResult(t *testing.T) {
	if outcomeFromClientResult("OK") != auditAllow {
		t.Fatal("OK should be allow")
	}
	if outcomeFromClientResult("NOT_FOUND") != auditAllow {
		t.Fatal("NOT_FOUND should be allow")
	}
	if outcomeFromClientResult("NOT_LEADER") != auditDeny {
		t.Fatal("NOT_LEADER should be deny")
	}
	if outcomeFromClientResult("ERROR: bad") != auditError {
		t.Fatal("ERROR should be error")
	}
}

func TestEmitClientAudit_Schema(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	emitClientAudit(log, clientAudit{
		Event:   auditEventClientMutate,
		Outcome: auditAllow,
		Node:    "node1",
		Term:    3,
		Remote:  "127.0.0.1:12345",
		Action:  "PUT",
		TraceID: "abc",
	})

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("audit line not JSON: %v", err)
	}
	if line["msg"] != "security_audit" {
		t.Fatalf("msg=%v", line["msg"])
	}
	if line["audit"] != true {
		t.Fatalf("audit=%v", line["audit"])
	}
	if line["event"] != auditEventClientMutate {
		t.Fatalf("event=%v", line["event"])
	}
	if line["outcome"] != "allow" {
		t.Fatalf("outcome=%v", line["outcome"])
	}
	if line["actor"] != actorUnauthenticated {
		t.Fatalf("actor=%v", line["actor"])
	}
	if line["action"] != "PUT" {
		t.Fatalf("action=%v", line["action"])
	}
	raw := buf.String()
	if strings.Contains(raw, "secret") || strings.Contains(raw, "mykey") {
		t.Fatalf("audit line leaked payload: %s", raw)
	}
}

func TestRequestIDFromCommand(t *testing.T) {
	if got := requestIDFromCommand(`PUT foo bar`); got != "" {
		t.Fatalf("plain text should omit request_id, got %q", got)
	}
	if got := requestIDFromCommand(`{"clientId":"c1","requestId":42,"op":"PUT k v"}`); got != "42" {
		t.Fatalf("json request_id=%q", got)
	}
}

func TestEmitClientAudit_MembershipEvent(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	emitClientAudit(log, clientAudit{
		Event:   auditEventClientMembership,
		Outcome: auditDeny,
		Node:    "raft-kv-0",
		Remote:  "10.0.0.1:8080",
		Action:  "ADD_SERVER",
	})
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["event"] != auditEventClientMembership {
		t.Fatalf("event=%v", line["event"])
	}
}
