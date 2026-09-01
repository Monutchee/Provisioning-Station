// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"testing"
	"time"
)

func TestSerialAttachTokenIsOneTimeAndClaimWinsExpiryRace(t *testing.T) {
	sessions := newSerialSessions()
	session := &serialSession{ID: "session", Token: "secret", ExpiresUTC: time.Now().Add(time.Minute)}
	sessions.items[session.ID] = session
	if claimed := sessions.claim(session.ID, "wrong"); claimed != nil {
		t.Fatal("wrong attach token was accepted")
	}
	if claimed := sessions.claim(session.ID, "secret"); claimed != session {
		t.Fatal("valid attach token was rejected")
	}
	if claimed := sessions.claim(session.ID, "secret"); claimed != nil {
		t.Fatal("attach token was accepted twice")
	}
	sessions.mutex.Lock()
	session.ExpiresUTC = time.Now().Add(-time.Second)
	sessions.mutex.Unlock()
	if expired := sessions.expire(session.ID); expired != nil {
		t.Fatal("expiry removed a session after it was claimed")
	}
	if removed := sessions.remove(session.ID); removed != session {
		t.Fatal("claimed session was not retained for stream cleanup")
	}
}
