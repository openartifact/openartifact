// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package passwd

import (
	"encoding/base64"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeIDKey replaces the Argon2id derivation: it records how many derivations
// run at once and holds each until the test releases it.
type fakeIDKey struct {
	inflight, peak atomic.Int32
	entered        chan struct{}
	gate           chan struct{}
}

func newFakeIDKey(n int) *fakeIDKey {
	return &fakeIDKey{entered: make(chan struct{}, n), gate: make(chan struct{})}
}

func (f *fakeIDKey) derive(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
	cur := f.inflight.Add(1)

	for {
		p := f.peak.Load()
		if cur <= p || f.peak.CompareAndSwap(p, cur) {
			break
		}
	}

	f.entered <- struct{}{}

	<-f.gate
	f.inflight.Add(-1)

	return make([]byte, keyLen)
}

// swapIDKey installs f for the test and restores the real derivation and the
// default cap afterwards.
func swapIDKey(t *testing.T, f *fakeIDKey) {
	t.Helper()

	orig := idKey
	idKey = f.derive

	t.Cleanup(func() {
		idKey = orig

		SetMaxConcurrent(0)
	})
}

func encodedZeroHash() string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		b64.EncodeToString(make([]byte, argonSaltLen)), b64.EncodeToString(make([]byte, argonKeyLen)))
}

func TestConcurrencyNeverExceedsTheCap(t *testing.T) {
	const callers = 24

	tests := []struct {
		name string
		cap  int
		call func() error
	}{
		{"verify cap 1", 1, func() error { _, err := VerifyPassword("a password long enough", encodedZeroHash()); return err }},
		{"verify cap 3", 3, func() error { _, err := VerifyPassword("a password long enough", encodedZeroHash()); return err }},
		{"hash cap 2", 2, func() error { _, err := HashPassword("a password long enough"); return err }},
		{"burn shares the cap", 2, func() error { BurnPasswordVerification("a password long enough"); return nil }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "burn shares the cap" {
				// The decoy hash is made with the real derivation, once.
				BurnPasswordVerification("warm the decoy hash")
			}

			f := newFakeIDKey(callers)
			swapIDKey(t, f)
			SetMaxConcurrent(tc.cap)

			var wg sync.WaitGroup

			errs := make(chan error, callers)

			for range callers {
				wg.Go(func() { errs <- tc.call() })
			}

			// Let each slot holder in, one at a time: once `cap` derivations run,
			// every other caller has to wait for one of them to finish.
			for range callers {
				<-f.entered
				runtime.Gosched()
				f.gate <- struct{}{}
			}

			wg.Wait()
			close(errs)

			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}

			if got := int(f.peak.Load()); got > tc.cap {
				t.Errorf("%d derivations ran at once, cap is %d", got, tc.cap)
			}
		})
	}
}

// With every slot taken a further caller waits, and takes a slot the moment one
// is freed; a slot is freed even when the derivation panics.
func TestSlotIsFreedOnPanic(t *testing.T) {
	orig := idKey

	t.Cleanup(func() {
		idKey = orig

		SetMaxConcurrent(0)
	})

	SetMaxConcurrent(1)

	idKey = func(_, _ []byte, _, _ uint32, _ uint8, _ uint32) []byte { panic("boom") }

	func() {
		defer func() { _ = recover() }()

		_, _ = VerifyPassword("a password long enough", encodedZeroHash())
	}()

	f := newFakeIDKey(1)
	idKey = f.derive

	close(f.gate)

	if ok, err := VerifyPassword("a password long enough", encodedZeroHash()); err != nil || !ok {
		t.Fatalf("VerifyPassword after a panic = %v, %v (the slot leaked)", ok, err)
	}
}

func TestSetMaxConcurrent(t *testing.T) {
	t.Cleanup(func() { SetMaxConcurrent(0) })

	tests := []struct {
		name string
		set  int
		want int
	}{
		{"explicit", 7, 7},
		{"one", 1, 1},
		{"zero is the default", 0, DefaultMaxConcurrent()},
		{"negative is the default", -3, DefaultMaxConcurrent()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			SetMaxConcurrent(tc.set)

			if got := cap(*argonSlots.Load()); got != tc.want {
				t.Errorf("cap = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDefaultMaxConcurrentIsClamped(t *testing.T) {
	if got := DefaultMaxConcurrent(); got < minConcurrent || got > maxConcurrent {
		t.Errorf("DefaultMaxConcurrent() = %d, want %d..%d (NumCPU %d)", got, minConcurrent, maxConcurrent, runtime.NumCPU())
	}
}

// BenchmarkVerifyPasswordFlood is a login flood: 64 simultaneous real
// verifications. cap=20 is what a 20-thread host allowed before the clamp.
func BenchmarkVerifyPasswordFlood(b *testing.B) {
	encoded, err := HashPassword("a password long enough")
	if err != nil {
		b.Fatal(err)
	}

	for _, n := range []int{20, 0} {
		b.Run(fmt.Sprintf("cap=%d", n), func(b *testing.B) {
			SetMaxConcurrent(n)
			b.Cleanup(func() { SetMaxConcurrent(0) })
			b.ReportAllocs()

			var peak uint64

			for range b.N {
				var wg sync.WaitGroup

				for range 64 {
					wg.Go(func() { _, _ = VerifyPassword("a password long enough", encoded) })
				}

				wg.Wait()

				var ms runtime.MemStats

				runtime.ReadMemStats(&ms)
				peak = max(peak, ms.HeapSys)
			}

			b.ReportMetric(float64(peak)/(1<<20), "heapsys-MiB")
		})
	}
}
