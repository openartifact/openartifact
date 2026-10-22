// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
)

// The server slides the idle expiry on every request; the browser cookie must
// outlive it (absolute lifetime) or active users are logged out at the idle
// window regardless of activity.
func TestSessionCookiesUseAbsoluteLifetime(t *testing.T) {
	want := int(auth.SessionAbsoluteLifetime().Seconds())
	if want <= int(auth.SessionDuration.Seconds()) {
		t.Fatalf("absolute lifetime %ds must exceed idle window %ds", want, int(auth.SessionDuration.Seconds()))
	}

	rec := httptest.NewRecorder()
	setSessionCookie(rec, false, "v")
	setCSRFCookie(rec, false, "t")

	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("got %d cookies, want 2", len(cookies))
	}

	for _, c := range cookies {
		if c.MaxAge != want {
			t.Errorf("%s MaxAge = %d, want %d", c.Name, c.MaxAge, want)
		}
	}
}
