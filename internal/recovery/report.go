// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"sync"
	"time"
)

// maxListed bounds each itemised list of a Report; counters stay exact.
const maxListed = 1000

// Exit codes of `openartifact rebuild`.
const (
	ExitOK = 0
	// ExitFailed: something recoverable failed (an invalid sidecar, a row the
	// database rejected, a post-step error), or the run itself broke.
	ExitFailed = 1
	// ExitIncomplete: the run finished but some rows cannot be recovered
	// (their blob bytes are gone from storage, a parent row is missing, or the
	// row collides with a different existing row).
	ExitIncomplete = 2
)

// Item names one row or blob in a Report.
type Item struct {
	Table  string `json:"table,omitempty"`
	Key    string `json:"key,omitempty"`
	Blob   string `json:"blob,omitempty"`
	Reason string `json:"reason"`
}

// TableReport is the per-table outcome.
type TableReport struct {
	Table string `json:"table"`
	// Found is the number of distinct rows with a valid sidecar.
	Found int `json:"found"`
	// Inserted is how many rows were (or, in a dry run, would be) inserted.
	Inserted int `json:"inserted"`
	// AlreadyPresent is how many rows existed already (left untouched).
	AlreadyPresent int `json:"already_present"`
	// Unrecoverable rows could not be restored for a reason in the Unrecoverable list.
	Unrecoverable int `json:"unrecoverable"`
	// Failed rows were rejected by the database.
	Failed int `json:"failed"`
}

// Report is the result of a rebuild run (also written with --report).
type Report struct {
	DryRun        bool      `json:"dry_run"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	SchemaVersion int       `json:"schema_version"`

	// Storage listing.
	ObjectsListed    int `json:"objects_listed"`
	SidecarCandidate int `json:"sidecar_candidates"`
	SidecarsValid    int `json:"sidecars_valid"`
	// SidecarsUnsupported are valid-looking sidecars of a newer format or for a
	// table this release does not know; they are skipped, not failed.
	SidecarsUnsupported int `json:"sidecars_unsupported"`
	SidecarsInvalid     int `json:"sidecars_invalid"`
	// Superseded are older sidecars of a row that has a newer one.
	Superseded int `json:"superseded_sidecars"`
	Pruned     int `json:"pruned_sidecars"`

	Tables []TableReport `json:"tables"`

	// UnreferencedBlobs counts storage objects that no recovered blobs row names
	// (neither a sidecar nor a referenced blob): garbage or data of tables that
	// are not journaled.
	UnreferencedBlobs int `json:"unreferenced_blobs"`

	ReposRecovered []string `json:"repositories_recovered"`

	Invalid       []Item `json:"invalid_sidecars,omitempty"`
	Unrecoverable []Item `json:"unrecoverable,omitempty"`
	Failures      []Item `json:"failures,omitempty"`
	MissingBlobs  []Item `json:"missing_blobs,omitempty"`

	UnrecoverableCount int `json:"unrecoverable_count"`
	FailureCount       int `json:"failure_count"`
	MissingBlobCount   int `json:"missing_blob_count"`

	// PostSteps records the post-restore steps that ran ("storage usage",
	// "metadata regeneration").
	PostSteps []string `json:"post_steps,omitempty"`

	// Warnings are conditions that do not fail the run but that the operator
	// should look at (for example a store without any sidecar).
	Warnings []string `json:"warnings,omitempty"`

	// NotRecoverable documents what the rebuild never restores.
	NotRecoverable []string `json:"not_recoverable"`

	mu sync.Mutex `json:"-"`
}

// notRecoverable is the fixed list of data classes a rebuild cannot bring back.
var notRecoverable = []string{ //nolint:gochecknoglobals // constant text
	"users, roles, permissions, tokens and sessions (re-apply with `oa apply` or restore a database backup)",
	"audit logs, webhooks, license/cleanup policies and other configuration that is not package data",
	"upstream repository passwords (re-enter them)",
	"download counters, last-used timestamps and other volatile statistics",
	"blobs held only on a non-primary tier, when rebuilding from the primary store alone",
}

func (r *Report) table(name string) *TableReport {
	for i := range r.Tables {
		if r.Tables[i].Table == name {
			return &r.Tables[i]
		}
	}

	r.Tables = append(r.Tables, TableReport{Table: name})

	return &r.Tables[len(r.Tables)-1]
}

func (r *Report) addUnrecoverable(it Item) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.UnrecoverableCount++

	if it.Table != "" {
		r.table(it.Table).Unrecoverable++
	}

	if len(r.Unrecoverable) < maxListed {
		r.Unrecoverable = append(r.Unrecoverable, it)
	}
}

func (r *Report) addFailure(it Item) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.FailureCount++

	if it.Table != "" {
		r.table(it.Table).Failed++
	}

	if len(r.Failures) < maxListed {
		r.Failures = append(r.Failures, it)
	}
}

func (r *Report) addMissingBlob(it Item) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.MissingBlobCount++

	if len(r.MissingBlobs) < maxListed {
		r.MissingBlobs = append(r.MissingBlobs, it)
	}
}

func (r *Report) addInvalid(it Item) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.SidecarsInvalid++

	if len(r.Invalid) < maxListed {
		r.Invalid = append(r.Invalid, it)
	}
}

// ExitCode maps the report to the process exit status.
func (r *Report) ExitCode() int {
	switch {
	case r.FailureCount > 0 || r.SidecarsInvalid > 0:
		return ExitFailed
	case r.UnrecoverableCount > 0:
		return ExitIncomplete
	}

	return ExitOK
}
