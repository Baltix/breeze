package heartbeat

import (
	"github.com/breeze-rmm/agent/internal/sessionbroker"
)

// activeRemoteSessionOverSignInConsole returns the WTS session id of an
// active, signed-in RDP session that a desktop start with no target should use
// instead of the console, or "" to keep the console.
//
// It applies only when the console is at the sign-in screen: enumerated, not
// disconnected, and with no user signed in (a username that could not be read
// does not count as nobody). A technician who pressed Connect Desktop on a
// server whose console shows the sign-in screen while an administrator is
// working over RDP wants that administrator's desktop (#8434, reporter test
// 3). An explicitly targeted session never reaches this; the viewer's session
// switcher still offers the console.
//
// Only sessions for which capable reports a capture-capable helper qualify,
// and the first in WTS enumeration order wins.
func activeRemoteSessionOverSignInConsole(consoleID string, detected []sessionbroker.DetectedSession, capable func(winSession string) bool) string {
	consoleAtSignIn := false
	for _, ds := range detected {
		if ds.Session == consoleID {
			consoleAtSignIn = ds.Username == "" && !ds.UsernameUnknown && ds.State != "disconnected"
			break
		}
	}
	if !consoleAtSignIn {
		return ""
	}
	for _, ds := range detected {
		if ds.Session == consoleID || ds.Type != "rdp" || ds.State != "active" || ds.Username == "" {
			continue
		}
		if capable(ds.Session) {
			return ds.Session
		}
	}
	return ""
}

// activeRemoteHelperOverSignInConsole is the live wiring of
// activeRemoteSessionOverSignInConsole for findActiveHelper: it reads the WTS
// session list and the connected helpers, and returns the helper to use, or
// nil to keep the existing console-first choice. Windows only by the caller.
func (h *Heartbeat) activeRemoteHelperOverSignInConsole(consoleID string) *sessionbroker.Session {
	detected, err := sessionbroker.NewSessionDetector().ListSessions()
	if err != nil {
		log.Debug("findActiveHelper: could not list sessions for the sign-in console check", "error", err.Error())
		return nil
	}
	helpers := make(map[string]*sessionbroker.Session)
	for _, s := range h.sessionBroker.SessionsWithScope("desktop") {
		caps := s.GetCapabilities()
		if caps == nil || !caps.CanCapture || isWinSessionDisconnected(s.WinSessionID) {
			continue
		}
		if _, seen := helpers[s.WinSessionID]; !seen {
			helpers[s.WinSessionID] = s
		}
	}
	id := activeRemoteSessionOverSignInConsole(consoleID, detected, func(w string) bool {
		_, ok := helpers[w]
		return ok
	})
	if id == "" {
		return nil
	}
	picked := helpers[id]
	log.Warn("findActiveHelper: console is at the sign-in screen; picked the active RDP session",
		"winSession", picked.WinSessionID, "helperSession", picked.SessionID, "consoleID", consoleID)
	return picked
}
