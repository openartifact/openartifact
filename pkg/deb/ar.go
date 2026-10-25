// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors for AR archive parsing.
var (
	ErrInvalidARMagic     = errors.New("invalid AR archive magic")
	ErrInvalidHeaderSize  = errors.New("invalid header size")
	ErrInvalidModTime     = errors.New("invalid modification time")
	ErrInvalidOwnerID     = errors.New("invalid owner ID")
	ErrInvalidGroupID     = errors.New("invalid group ID")
	ErrInvalidMode        = errors.New("invalid mode")
	ErrInvalidSize        = errors.New("invalid size")
	ErrARMemberTooLarge   = errors.New("AR member size exceeds maximum allowed")
	ErrInvalidARHeaderEnd = errors.New("invalid AR header ending marker")
)

// ARHeader represents the header of an AR archive member.
type ARHeader struct {
	Name    string
	ModTime time.Time
	OwnerID int
	GroupID int
	Mode    int
	Size    int64
}

const (
	arMagic       = "!<arch>\n"
	arHeaderSize  = 60
	arMemberAlign = 2
)

// parseARHeader parses a 60-byte AR member header.
func parseARHeader(buf []byte) (ARHeader, error) {
	if len(buf) != arHeaderSize {
		return ARHeader{}, fmt.Errorf("%w: %d", ErrInvalidHeaderSize, len(buf))
	}

	header := ARHeader{}

	// Name (16 bytes)
	name := strings.TrimRight(string(buf[0:16]), " ")
	// Handle extended filename format (name ends with /)
	name = strings.TrimSuffix(name, "/")
	header.Name = name

	// Modification time (12 bytes)
	modTimeStr := strings.TrimSpace(string(buf[16:28]))

	modTime, err := strconv.ParseInt(modTimeStr, 10, 64)
	if err != nil {
		return ARHeader{}, fmt.Errorf("%w: %s", ErrInvalidModTime, modTimeStr)
	}

	header.ModTime = time.Unix(modTime, 0)

	// Owner ID (6 bytes)
	ownerStr := strings.TrimSpace(string(buf[28:34]))

	owner, err := strconv.Atoi(ownerStr)
	if err != nil {
		return ARHeader{}, fmt.Errorf("%w: %s", ErrInvalidOwnerID, ownerStr)
	}

	header.OwnerID = owner

	// Group ID (6 bytes)
	groupStr := strings.TrimSpace(string(buf[34:40]))

	group, err := strconv.Atoi(groupStr)
	if err != nil {
		return ARHeader{}, fmt.Errorf("%w: %s", ErrInvalidGroupID, groupStr)
	}

	header.GroupID = group

	// Mode (8 bytes)
	modeStr := strings.TrimSpace(string(buf[40:48]))

	mode, err := strconv.ParseInt(modeStr, 8, 32)
	if err != nil {
		return ARHeader{}, fmt.Errorf("%w: %s", ErrInvalidMode, modeStr)
	}

	header.Mode = int(mode)

	// Size (10 bytes)
	sizeStr := strings.TrimSpace(string(buf[48:58]))

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return ARHeader{}, fmt.Errorf("%w: %s", ErrInvalidSize, sizeStr)
	}

	header.Size = size

	// Ending marker (2 bytes) - should be "`\n"
	if buf[58] != '`' || buf[59] != '\n' {
		return ARHeader{}, ErrInvalidARHeaderEnd
	}

	return header, nil
}
