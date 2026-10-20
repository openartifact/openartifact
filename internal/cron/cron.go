// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cron parses standard 5-field cron expressions
// ("minute hour day-of-month month day-of-week") and computes fire times in UTC.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression.
type Schedule struct {
	minute, hour, dom, month, dow uint64 // bitmasks
	domAny, dowAny                bool   // field was "*" (or "*/n" with full range start)
}

// descriptor expands an @descriptor into its 5-field form.
func descriptor(name string) (string, bool) {
	switch name {
	case "@yearly", "@annually":
		return "0 0 1 1 *", true
	case "@monthly":
		return "0 0 1 * *", true
	case "@weekly":
		return "0 0 * * 0", true
	case "@daily", "@midnight":
		return "0 0 * * *", true
	case "@hourly":
		return "0 * * * *", true
	}

	return "", false
}

// ErrInvalid is wrapped by every Parse error.
var ErrInvalid = errors.New("invalid cron expression")

var (
	errBadStep    = errors.New("bad step")
	errBadValue   = errors.New("bad value")
	errOutOfRange = errors.New("value out of range")
)

// Parse parses a 5-field cron expression or one of the @descriptors.
func Parse(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	if d, ok := descriptor(strings.ToLower(expr)); ok {
		expr = d
	}

	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w: expected 5 fields, got %d", ErrInvalid, len(fields))
	}

	s := &Schedule{}

	var err error
	if s.minute, _, err = parseField(fields[0], 0, 59); err != nil {
		return nil, fmt.Errorf("%w: minute: %w", ErrInvalid, err)
	}

	if s.hour, _, err = parseField(fields[1], 0, 23); err != nil {
		return nil, fmt.Errorf("%w: hour: %w", ErrInvalid, err)
	}

	if s.dom, s.domAny, err = parseField(fields[2], 1, 31); err != nil {
		return nil, fmt.Errorf("%w: day-of-month: %w", ErrInvalid, err)
	}

	if s.month, _, err = parseField(fields[3], 1, 12); err != nil {
		return nil, fmt.Errorf("%w: month: %w", ErrInvalid, err)
	}

	if s.dow, s.dowAny, err = parseField(fields[4], 0, 7); err != nil {
		return nil, fmt.Errorf("%w: day-of-week: %w", ErrInvalid, err)
	}

	if s.dow&(1<<7) != 0 { // 7 == Sunday
		s.dow |= 1
	}

	s.dow &^= 1 << 7

	return s, nil
}

// parseField parses one field into a bitmask; star reports whether the field
// is an unrestricted wildcard.
func parseField(field string, lo, hi int) (uint64, bool, error) {
	var (
		mask uint64
		star bool
		err  error
	)

	for part := range strings.SplitSeq(field, ",") {
		rangePart, stepStr, hasStep := strings.Cut(part, "/")

		step := 1

		if hasStep {
			step, err = strconv.Atoi(stepStr)
			if err != nil || step < 1 {
				return 0, false, fmt.Errorf("%w %q", errBadStep, stepStr)
			}
		}

		var start, end int

		switch {
		case rangePart == "*":
			start, end = lo, hi
			if !hasStep {
				star = true
			}
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")

			start, err = strconv.Atoi(a)
			if err != nil {
				return 0, false, fmt.Errorf("%w %q", errBadValue, a)
			}

			end, err = strconv.Atoi(b)
			if err != nil {
				return 0, false, fmt.Errorf("%w %q", errBadValue, b)
			}
		default:
			start, err = strconv.Atoi(rangePart)
			if err != nil {
				return 0, false, fmt.Errorf("%w %q", errBadValue, rangePart)
			}

			end = start
			if hasStep {
				end = hi
			}
		}

		if start < lo || end > hi || start > end {
			return 0, false, fmt.Errorf("%w %d-%d", errOutOfRange, lo, hi)
		}

		for v := start; v <= end; v += step {
			mask |= 1 << uint(v)
		}
	}

	return mask, star, nil
}

func (s *Schedule) dayMatches(t time.Time) bool {
	domMatch := s.dom&(1<<uint(t.Day())) != 0
	dowMatch := s.dow&(1<<uint(t.Weekday())) != 0

	// Standard cron: if both day fields are restricted, either may match.
	if !s.domAny && !s.dowAny {
		return domMatch || dowMatch
	}

	return domMatch && dowMatch
}

// Next returns the first fire time strictly after t (in UTC), or the zero
// time if none exists within five years.
func (s *Schedule) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)

	for t.Before(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}

		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}

		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}

		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}

		return t
	}

	return time.Time{}
}
