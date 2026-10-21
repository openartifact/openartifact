// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

const (
	stateFileMode  = 0o600
	maxStateLineSz = 1 << 20
)

// importState remembers which assets were fully imported so an interrupted run
// resumes where it stopped. It is an append-only JSON-lines file; an entry is
// written only after the asset (bytes, checksum verification and properties) is
// complete. With no path it is purely in-memory.
type importState struct {
	mu   sync.Mutex
	done map[string]string // key -> source sha256
	f    *os.File
}

type stateRecord struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

// openImportState loads the state file (creating it when missing).
func openImportState(path string) (*importState, error) {
	s := &importState{done: map[string]string{}}
	if path == "" {
		return s, nil
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, stateFileMode)
	if err != nil {
		return nil, fmt.Errorf("opening state file: %w", err)
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxStateLineSz)

	for sc.Scan() {
		var r stateRecord
		// A torn last line from a crash is simply not a record.
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Key != "" {
			s.done[r.Key] = r.SHA256
		}
	}

	if err := sc.Err(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("reading state file: %w", err)
	}

	s.f = f

	return s, nil
}

func stateKey(srcRepo, dstRepo, assetPath string) string {
	return srcRepo + "\x00" + dstRepo + "\x00" + assetPath
}

// isDone reports whether the asset was already imported with this content.
func (s *importState) isDone(key, sha string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	got, ok := s.done[key]

	return ok && got == sha
}

func (s *importState) markDone(key, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.done[key] = sha

	if s.f == nil {
		return nil
	}

	line, err := json.Marshal(stateRecord{Key: key, SHA256: sha})
	if err != nil {
		return err
	}

	if _, err := s.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("writing state file: %w", err)
	}

	return nil
}

func (s *importState) close() {
	if s.f != nil {
		_ = s.f.Close()
	}
}
