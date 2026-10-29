// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// relayStateMaxAge bounds how long a signed relay state stays valid: the
	// time a user needs to sign in at the identity provider.
	relayStateMaxAge = 10 * time.Minute
	// relayStateSkew tolerates a clock a little ahead of the signer's.
	relayStateSkew = time.Minute
	// relayStateDomain separates this MAC from every other use of the same key.
	relayStateDomain = "openartifact.relaystate.v1\x00"
)

var ErrInvalidRelayState = errors.New("invalid relay state")

// SignRelayState returns redirect with its signing time, MAC'd under key. The
// result is valid for ten minutes.
func SignRelayState(redirect, key string) string {
	return signRelayStateAt(redirect, key, time.Now())
}

// VerifyRelayState returns the redirect of a state SignRelayState made with key
// less than ten minutes ago.
func VerifyRelayState(encoded, key string) (string, error) {
	return verifyRelayStateAt(encoded, key, time.Now())
}

// relayStateMAC covers the domain prefix, the timestamp and the redirect; the
// timestamp is length-delimited by its NUL so it cannot bleed into the redirect.
func relayStateMAC(issued, redirect, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(relayStateDomain + issued + "\x00" + redirect))

	return hex.EncodeToString(mac.Sum(nil))
}

// signRelayStateAt encodes issued|redirect|mac; the redirect may itself hold a
// "|", the issue time and the hex MAC never do.
func signRelayStateAt(redirect, key string, now time.Time) string {
	issued := strconv.FormatInt(now.Unix(), 10)

	return base64.RawURLEncoding.EncodeToString([]byte(issued + "|" + redirect + "|" + relayStateMAC(issued, redirect, key)))
}

func verifyRelayStateAt(encoded, key string, now time.Time) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrInvalidRelayState
	}

	issued, rest, ok := strings.Cut(string(raw), "|")
	if !ok {
		return "", ErrInvalidRelayState
	}

	cut := strings.LastIndexByte(rest, '|')
	if cut < 0 {
		return "", ErrInvalidRelayState
	}

	redirect, gotSig := rest[:cut], rest[cut+1:]
	if !hmac.Equal([]byte(gotSig), []byte(relayStateMAC(issued, redirect, key))) {
		return "", ErrInvalidRelayState
	}

	at, err := strconv.ParseInt(issued, 10, 64)
	if err != nil {
		return "", ErrInvalidRelayState
	}

	age := now.Sub(time.Unix(at, 0))
	if age > relayStateMaxAge || age < -relayStateSkew {
		return "", ErrInvalidRelayState
	}

	return redirect, nil
}
