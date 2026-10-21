package heartbeat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// W09 (#8148) contract: every httputil.Do call in this package's non-test
// files is either gated by the per-route loop deferral (its function calls
// loopReady) or listed below with the reason it must never be deferred. A new
// ungated call fails here, so a new periodic sender cannot silently ignore a
// server's Retry-After, and a heartbeat can never be routed through the gate.
var loopDeferralExempt = map[string]string{
	"doHeartbeatPost":      "heartbeat: the watchdog restarts an agent whose LastHeartbeat is 3 min stale; overload answers feed #9146 liveness (D6)",
	"submitCommandResult":  "command result: never deferred (Global Constraints)",
	"SendElevationRequest": "elevation: interactive, a user is waiting; W08 protected",
	"postPamJSON":          "PAM reconciliation: own durable outbox and retry; W08 protected",
	"postChatReply":        "chat reply: interactive, a user is waiting on it; one attempt per user action",
}

// gatedHTTPFuncs are the functions that must gate and record. Listing them
// makes a removed gate fail by name, not only by count.
var gatedHTTPFuncs = []string{
	"sendInventoryData",     // every PUT /:id/<inventory> route (bulk/standard)
	"sendMonitoringResults", // PUT monitoring-results (standard)
	"sendProcessSample",     // POST process-sample (bulk)
	"submitPeripheralEvents",
	"sendBootPerformance",
	"sendReliabilityMetrics",
	"postMessageEvent", // POST message-events (standard; receipts are re-reported)
}

type doCall struct {
	file, fn string
	body     string
}

func scanHTTPDoCalls(t *testing.T) []doCall {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []doCall
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range af.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Do" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "httputil" {
						found = true
					}
				}
				return true
			})
			if found {
				body := string(src[fset.Position(fd.Body.Pos()).Offset:fset.Position(fd.Body.End()).Offset])
				out = append(out, doCall{file: f, fn: fd.Name.Name, body: body})
			}
		}
	}
	return out
}

func TestEveryAgentRouteCallIsGatedOrExempt(t *testing.T) {
	calls := scanHTTPDoCalls(t)
	if len(calls) < len(gatedHTTPFuncs)+len(loopDeferralExempt)-1 {
		t.Fatalf("found only %d httputil.Do functions; the scan is broken", len(calls))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.fn] = true
		gated := strings.Contains(c.body, "loopReady(")
		records := strings.Contains(c.body, "noteLoopAnswer(")
		if reason, exempt := loopDeferralExempt[c.fn]; exempt {
			if gated || records {
				t.Errorf("%s (%s) is exempt (%s) but touches the loop deferral", c.fn, c.file, reason)
			}
			continue
		}
		if !gated || !records {
			t.Errorf("%s (%s) calls httputil.Do without the loop deferral (gated=%v, records=%v): gate it with loopReady before collecting and noteLoopAnswer after Do, or add it to loopDeferralExempt with a reason",
				c.fn, c.file, gated, records)
		}
	}
	for _, fn := range gatedHTTPFuncs {
		if !seen[fn] {
			t.Errorf("gated sender %s no longer calls httputil.Do; update this contract", fn)
		}
	}
	for fn := range loopDeferralExempt {
		if !seen[fn] {
			t.Errorf("exempt function %s no longer calls httputil.Do; update this contract", fn)
		}
	}
}

// Senders that collect, pop or claim state before they send must check the
// gate FIRST, or a deferred cycle loses what it collected (D5).
func TestCollectBeforeSendSendersGateFirst(t *testing.T) {
	src, err := os.ReadFile("heartbeat.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	cases := []struct {
		fn, before string
	}{
		{"func (h *Heartbeat) sendEventLogs()", "h.eventLogCol.Collect"},
		{"func (h *Heartbeat) sendRecoveryKeys()", "h.pendingRecoveryKeys = nil"},
		{"func (h *Heartbeat) sendReliabilityMetrics(", "h.reliabilityCol.Collect"},
		{"func (h *Heartbeat) sendProcessSample()", "tools.TopProcessSample"},
		{"func (h *Heartbeat) sendInventoryData(", "json.Marshal"},
	}
	for _, tc := range cases {
		start := strings.Index(s, tc.fn)
		if start < 0 {
			t.Errorf("%s not found", tc.fn)
			continue
		}
		body := s[start:]
		if end := strings.Index(body[1:], "\nfunc "); end > 0 {
			body = body[:end+1]
		}
		gate, act := strings.Index(body, "loopReady("), strings.Index(body, tc.before)
		if gate < 0 || act < 0 || gate > act {
			t.Errorf("%s: loopReady at %d must come before %q at %d", tc.fn, gate, tc.before, act)
		}
	}
	// The daily patch scan is claimed under h.mu; the route check runs first,
	// outside the lock (serverURL takes h.mu).
	if !strings.Contains(s, "shouldSendPatch := patchRouteReady && h.claimPatchScanLocked(") {
		t.Error("patch scan claim is not gated by the patches/pending route")
	}
}
