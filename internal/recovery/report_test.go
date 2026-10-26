// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"strconv"
	"sync"
	"testing"
)

// TestReportKeepsExactCountersBeyondTheListCap: the itemised lists are bounded
// but the counters and the per-table totals stay exact, also under concurrency.
func TestReportKeepsExactCountersBeyondTheListCap(t *testing.T) {
	const total = maxListed + 250

	rep := &Report{}

	var wg sync.WaitGroup

	for i := range total {
		wg.Go(func() {
			it := Item{Table: "raw_artifacts", Key: strconv.Itoa(i), Reason: "r"}
			rep.addUnrecoverable(it)
			rep.addFailure(it)
			rep.addMissingBlob(it)
			rep.addInvalid(Item{Blob: strconv.Itoa(i), Reason: "r"})
		})
	}

	wg.Wait()

	if rep.UnrecoverableCount != total || rep.FailureCount != total || rep.MissingBlobCount != total || rep.SidecarsInvalid != total {
		t.Errorf("counters = %d/%d/%d/%d, want %d each", rep.UnrecoverableCount, rep.FailureCount, rep.MissingBlobCount, rep.SidecarsInvalid, total)
	}

	if len(rep.Unrecoverable) != maxListed || len(rep.Failures) != maxListed || len(rep.MissingBlobs) != maxListed || len(rep.Invalid) != maxListed {
		t.Errorf("lists = %d/%d/%d/%d, want %d each", len(rep.Unrecoverable), len(rep.Failures), len(rep.MissingBlobs), len(rep.Invalid), maxListed)
	}

	if len(rep.Tables) != 1 || rep.Tables[0].Unrecoverable != total || rep.Tables[0].Failed != total {
		t.Errorf("tables = %+v, want one table with %d unrecoverable and failed rows", rep.Tables, total)
	}
}

// TestUnsupportedSidecarsAreNotInvalid: sidecars of a newer format or an
// unknown table are skipped without failing the run.
func TestUnsupportedSidecarsAreNotInvalid(t *testing.T) {
	r := &Rebuilder{}
	rep := &Report{}

	r.classifyBad(rep, "a", ErrSidecarVersion)
	r.classifyBad(rep, "b", ErrSidecarTable)
	r.classifyBad(rep, "c", ErrSidecarDigest)

	if rep.SidecarsUnsupported != 2 || rep.SidecarsInvalid != 1 || rep.ExitCode() != ExitFailed {
		t.Errorf("unsupported=%d invalid=%d exit=%d, want 2, 1 and a failed run", rep.SidecarsUnsupported, rep.SidecarsInvalid, rep.ExitCode())
	}
}
