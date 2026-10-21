package agentapp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/viper"

	"github.com/breeze-rmm/agent/internal/config"
)

// #9540: a new installer sent to a host that already has an enrolled agent
// must take effect instead of exiting 0 with "already enrolled". The MSI
// BootstrapEnroll CA also runs on upgrades, so the decision is keyed on the
// bootstrap token that enrolled this agent (config.BootstrapTokenFingerprint):
//
//	same token       → the original download re-ran (upgrade): skip, no HTTP
//	different token  → redeem it; failure keeps the existing enrollment (soft),
//	                   success re-enrolls under the new identity
//	no token         → skip (unchanged)

const existingAgentID = "0f0e0d0c-0b0a-4908-8706-050403020100"

// writeEnrolledConfig plants an enrolled agent.yaml (+ the fingerprint of the
// token that enrolled it, when non-empty) and points runBootstrap at it.
func writeBootstrapEnrolledConfig(t *testing.T, enrolledWithToken string) string {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.yaml")
	body := "agent_id: " + existingAgentID + "\nlog_file: " + filepath.ToSlash(filepath.Join(dir, "agent.log")) + "\n"
	if enrolledWithToken != "" {
		body += "bootstrap_token_fingerprint: " + config.BootstrapTokenFingerprint(enrolledWithToken) + "\n"
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// bootstrapSinks captures the durable sinks and the exit code around a
// runBootstrap call against cfgPath with the given install data.
func bootstrapSinks(t *testing.T, cfgPath, installData string) (exitCode *int, lastError, eventErr, eventWarn *string) {
	t.Helper()
	origCfg, origData, origQuiet := cfgFile, bootstrapInstallData, quietEnroll
	origExit, origWrite, origEvErr, origEvWarn := osExit, writeLastErrorFile, eventLogError, eventLogWarning
	t.Cleanup(func() {
		cfgFile, bootstrapInstallData, quietEnroll = origCfg, origData, origQuiet
		osExit, writeLastErrorFile, eventLogError, eventLogWarning = origExit, origWrite, origEvErr, origEvWarn
	})
	cfgFile, quietEnroll, bootstrapInstallData = cfgPath, true, installData
	code := -1
	var le, ee, ew string
	osExit = func(c int) {
		code = c
		panic(fmt.Sprintf("test exit %d", c))
	}
	writeLastErrorFile = func(line string) { le = line }
	eventLogError = func(_, m string) { ee = m }
	eventLogWarning = func(_, m string) { ew = m }
	return &code, &le, &ee, &ew
}

func loadAgentID(t *testing.T, cfgPath string) string {
	t.Helper()
	viper.Reset()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg.AgentID
}

func TestRunBootstrap_SameTokenOnEnrolledHostSkipsWithoutHTTP(t *testing.T) {
	cfgPath := writeBootstrapEnrolledConfig(t, "TESTTOKEN1")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	code, _, _, _ := bootstrapSinks(t, cfgPath, `C:\dl\breeze-agent.msi|TESTTOKEN1|`+srv.URL)

	runBootstrapRecovering()

	if n := hits.Load(); n != 0 {
		t.Fatalf("bootstrap endpoint contacted %d time(s) for the installer that enrolled this host — an upgrade with the original download must not spend a token slot", n)
	}
	if *code != -1 {
		t.Fatalf("exit code = %d, want no exit (upgrade proceeds)", *code)
	}
	if got := loadAgentID(t, cfgPath); got != existingAgentID {
		t.Fatalf("agent_id = %q, want untouched %q", got, existingAgentID)
	}
}

func TestRunBootstrap_NewTokenRedeemFailureKeepsExistingEnrollment(t *testing.T) {
	for _, stored := range []string{"OLDTOKEN00", "" /* fleet enrolled before the fingerprint existed */} {
		t.Run(fmt.Sprintf("stored=%q", stored), func(t *testing.T) {
			cfgPath := writeBootstrapEnrolledConfig(t, stored)
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusNotFound) // expired / exhausted token
			}))
			defer srv.Close()
			code, lastErr, evErr, evWarn := bootstrapSinks(t, cfgPath, `C:\dl\breeze-agent.msi|NEWTOKEN01|`+srv.URL)

			runBootstrapRecovering()

			if n := hits.Load(); n != 1 {
				t.Fatalf("redeem attempts = %d, want 1 (a different token is an operator's intent to re-enroll)", n)
			}
			if *code != -1 {
				t.Fatalf("exit code = %d, want no exit — a stale token on an enrolled host must never roll an install back", *code)
			}
			if *evErr != "" || *lastErr == "" || *evWarn == "" {
				t.Fatalf("want a durable WARNING (not error): lastErr=%q evWarn=%q evErr=%q", *lastErr, *evWarn, *evErr)
			}
			if !strings.Contains(*evWarn, "existing enrollment") {
				t.Fatalf("warning must say the existing enrollment was kept: %q", *evWarn)
			}
			if got := loadAgentID(t, cfgPath); got != existingAgentID {
				t.Fatalf("agent_id = %q, want untouched %q", got, existingAgentID)
			}
		})
	}
}

func TestRunBootstrap_NewTokenRedeemSuccessReenrolls(t *testing.T) {
	cfgPath := writeBootstrapEnrolledConfig(t, "OLDTOKEN00")
	var redeemed string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/installer/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		redeemed = r.Header.Get("X-Breeze-Bootstrap-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"serverUrl":"","enrollmentKey":"child-key-abc","enrollmentSecret":"","siteId":"site1"}`))
	})
	mux.HandleFunc("/api/v1/agents/enroll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"1f1e1d1c-1b1a-4918-8716-151413121110","deviceId":"device-new-1","authToken":"tok","orgId":"org1","siteId":"site1","config":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	code, _, evErr, _ := bootstrapSinks(t, cfgPath, `C:\dl\breeze-agent.msi|NEWTOKEN01|`+srv.URL)

	runBootstrapRecovering()

	if *code != -1 {
		t.Fatalf("exit code = %d (event error %q), want no exit", *code, *evErr)
	}
	if redeemed != "NEWTOKEN01" {
		t.Fatalf("redeemed token = %q, want NEWTOKEN01", redeemed)
	}
	viper.Reset()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentID != "1f1e1d1c-1b1a-4918-8716-151413121110" {
		t.Fatalf("agent_id = %q, want the new identity 1f1e1d1c-1b1a-4918-8716-151413121110", cfg.AgentID)
	}
	if want := config.BootstrapTokenFingerprint("NEWTOKEN01"); cfg.BootstrapTokenFingerprint != want {
		t.Fatalf("bootstrap_token_fingerprint = %q, want %q so a re-run of this same installer skips", cfg.BootstrapTokenFingerprint, want)
	}
	if forceEnroll {
		t.Fatal("forceEnroll global leaked out of runBootstrap")
	}
}
