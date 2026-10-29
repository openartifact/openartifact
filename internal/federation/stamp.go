// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package federation replicates the artifacts of a repository between
// OpenArtifact instances (docs/design/federation.md): every member keeps an
// event log of its changes, written transactionally with them, and pulls the
// logs of its peers.
package federation

import (
	"bytes"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultMaxClockSkew is how far ahead of the local clock the wall time of
	// a peer's stamp may be (federation.maxClockSkew). The clock of a
	// repository never goes back, so one stamp from the far future would make
	// every later local change of the repository carry it and win against the
	// changes of every other member.
	DefaultMaxClockSkew = 5 * time.Minute

	// maxStampCounter bounds the logical counter of an incoming stamp. A real
	// counter only counts the changes within one microsecond; the clock adds
	// one to the counter it sees, so values near the int32 limit would
	// overflow it.
	maxStampCounter = 1 << 24
)

// validateStamp rejects a stamp that a well-behaved member cannot have
// produced: a negative or implausibly large counter, or a wall time more than
// maxSkew ahead of now. Stamps in the past are always fine.
func validateStamp(s Stamp, now time.Time, maxSkew time.Duration) error {
	if s.Wall < 0 || s.Counter < 0 || s.Counter > maxStampCounter {
		return fmt.Errorf("%w: counter %d, wall %d", errStampInvalid, s.Counter, s.Wall)
	}

	if limit := now.Add(maxSkew).UnixMicro(); s.Wall > limit {
		return fmt.Errorf("%w: wall time %d is past the local clock plus the allowed skew of %s", errStampInvalid, s.Wall, maxSkew)
	}

	return nil
}

// Stamp orders the changes of one unit across instances: a hybrid logical
// clock (wall microseconds, counter) with the origin instance as the last
// tie-breaker. The highest stamp wins; a stamp never compares equal for two
// different changes.
type Stamp struct {
	Wall    int64     `json:"wall"`
	Counter int32     `json:"counter"`
	Origin  uuid.UUID `json:"origin"`
}

// Compare returns -1, 0 or 1 as s is before, equal to or after o.
func (s Stamp) Compare(o Stamp) int {
	switch {
	case s.Wall != o.Wall:
		return cmpInt64(s.Wall, o.Wall)
	case s.Counter != o.Counter:
		return cmpInt64(int64(s.Counter), int64(o.Counter))
	default:
		return bytes.Compare(s.Origin[:], o.Origin[:])
	}
}

// IsZero reports whether the stamp is unset.
func (s Stamp) IsZero() bool {
	return s.Wall == 0 && s.Counter == 0 && s.Origin == uuid.Nil
}

func (s Stamp) String() string {
	return fmt.Sprintf("%d.%d@%s", s.Wall, s.Counter, s.Origin)
}

func cmpInt64(a, b int64) int {
	if a < b {
		return -1
	}

	return 1
}
