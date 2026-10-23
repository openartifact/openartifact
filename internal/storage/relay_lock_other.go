// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package storage

import "sync"

// Without advisory file locks the relay serialises uploads within this process
// only: one replica per staging directory.
var processLocks sync.Map // path -> *sync.RWMutex

func processLock(path string) *sync.RWMutex {
	l, _ := processLocks.LoadOrStore(path, new(sync.RWMutex))

	return l.(*sync.RWMutex) //nolint:forcetypeassert // only *sync.RWMutex is stored
}

func lockShared(path string) (func(), error) {
	l := processLock(path)
	l.RLock()

	return l.RUnlock, nil
}

func tryLockExclusive(path string) (func(), bool, error) {
	l := processLock(path)
	if !l.TryLock() {
		return nil, true, nil
	}

	return l.Unlock, false, nil
}
