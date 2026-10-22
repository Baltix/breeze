package heartbeat

import (
	"testing"

	"github.com/breeze-rmm/agent/internal/sessionbroker"
)

// #8434 test 3: the console sat at the sign-in screen (Connected, no user)
// while Administrator was Active over RDP in session 2, and Connect Desktop
// still targeted the empty console.
func TestActiveRemoteSessionOverSignInConsole(t *testing.T) {
	signInConsole := sessionbroker.DetectedSession{Session: "1", State: "connected", Type: "console"}
	activeRDP := sessionbroker.DetectedSession{Session: "2", State: "active", Type: "rdp", Username: "Administrator"}
	all := func(string) bool { return true }
	none := func(string) bool { return false }

	for _, tc := range []struct {
		name     string
		console  string
		detected []sessionbroker.DetectedSession
		capable  func(string) bool
		want     string
	}{
		{"console at sign-in, active RDP with a helper", "1",
			[]sessionbroker.DetectedSession{signInConsole, activeRDP}, all, "2"},
		{"RDP session has no capable helper", "1",
			[]sessionbroker.DetectedSession{signInConsole, activeRDP}, none, ""},
		{"someone is signed in at the console", "1",
			[]sessionbroker.DetectedSession{{Session: "1", State: "active", Type: "console", Username: "alice"}, activeRDP}, all, ""},
		// A failed WTS username query is "could not tell", not "nobody".
		{"console username unknown", "1",
			[]sessionbroker.DetectedSession{{Session: "1", State: "connected", Type: "console", UsernameUnknown: true}, activeRDP}, all, ""},
		{"RDP session disconnected", "1",
			[]sessionbroker.DetectedSession{signInConsole, {Session: "2", State: "disconnected", Type: "rdp", Username: "Administrator"}}, all, ""},
		{"RDP session with no user yet", "1",
			[]sessionbroker.DetectedSession{signInConsole, {Session: "2", State: "active", Type: "rdp"}}, all, ""},
		{"active non-RDP session is not a remote session", "1",
			[]sessionbroker.DetectedSession{signInConsole, {Session: "2", State: "active", Type: "console", Username: "bob"}}, all, ""},
		{"console not enumerated", "1",
			[]sessionbroker.DetectedSession{activeRDP}, all, ""},
		{"first capable active RDP session wins", "1",
			[]sessionbroker.DetectedSession{signInConsole,
				{Session: "2", State: "active", Type: "rdp", Username: "a"},
				{Session: "3", State: "active", Type: "rdp", Username: "b"}},
			func(s string) bool { return s == "3" }, "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := activeRemoteSessionOverSignInConsole(tc.console, tc.detected, tc.capable); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
