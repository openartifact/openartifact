// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Unit kinds. An artifact unit owns content; a ref unit points at another
// unit (a tag at a version or a manifest) and moves.
const (
	KindArtifact = "artifact"
	KindRef      = "ref"
)

// Event names logged for a unit.
const (
	EventArtifactAdded   = "artifact_added"
	EventArtifactDeleted = "artifact_deleted"
	EventRefMoved        = "ref_moved"
	EventMetadataChanged = "metadata_changed"
)

// Entry is one row of a repository's change log as served to peers: the latest
// event of a unit. Entries are listed in Seq order; a unit appears once, at
// the position of its last change.
type Entry struct {
	Seq        int64     `json:"seq"`
	UnitKey    string    `json:"unit_key"`
	Kind       string    `json:"kind"`
	Event      string    `json:"event"`
	Tombstone  bool      `json:"tombstone"`
	Stamp      Stamp     `json:"stamp"`
	ContentRef string    `json:"content_ref"`
	ChangedAt  time.Time `json:"changed_at"`
}

// BlobRef names a blob a unit references by content hash.
type BlobRef struct {
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type,omitempty"`
}

// Content is the replicated state of a live unit: everything a peer needs to
// recreate the rows (Data, format specific and opaque to the transport), the
// blobs to fetch first and the units that must exist before it (Deps).
type Content struct {
	Kind       string          `json:"kind"`
	Data       json.RawMessage `json:"data"`
	Blobs      []BlobRef       `json:"blobs,omitempty"`
	Deps       []string        `json:"deps,omitempty"`
	ContentRef string          `json:"content_ref"`
}

// UnitResponse is the body of GET /federation/v1/unit: the unit's current log
// row and, for a live unit, its content.
type UnitResponse struct {
	Entry   Entry    `json:"entry"`
	Content *Content `json:"content,omitempty"`
}

// InfoResponse is the body of GET /federation/v1/info.
type InfoResponse struct {
	Instance   uuid.UUID `json:"instance_id"`
	LogID      uuid.UUID `json:"log_id"`
	HeadSeq    int64     `json:"head_seq"`
	Repository string    `json:"repository"`
	Format     string    `json:"format"`
	Version    int       `json:"protocol_version"`
}

// LogResponse is the body of GET /federation/v1/log.
type LogResponse struct {
	LogID   uuid.UUID `json:"log_id"`
	HeadSeq int64     `json:"head_seq"`
	Entries []Entry   `json:"entries"`
	// More is true when entries beyond the returned page exist.
	More bool `json:"more"`
}

// ProtocolVersion is the version of the pull protocol this build speaks.
const ProtocolVersion = 1
