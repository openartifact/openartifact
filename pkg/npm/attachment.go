// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"errors"
	"fmt"
)

// ErrInvalidAttachment reports a malformed _attachments entry of a publish body.
var ErrInvalidAttachment = errors.New("invalid npm attachment")

// AttachmentContentType is the only content type npm publishes tarballs with.
const AttachmentContentType = "application/octet-stream"

// CheckAttachmentContentType validates the content_type of an attachment; ok
// is false when the field was absent or not a string.
func CheckAttachmentContentType(ct string, ok bool) error {
	if !ok {
		return fmt.Errorf("%w: content_type missing or not a string", ErrInvalidAttachment)
	}

	if ct != AttachmentContentType {
		return fmt.Errorf("%w: content_type must be %s, got %q", ErrInvalidAttachment, AttachmentContentType, ct)
	}

	return nil
}

// CheckAttachmentLength validates that the declared attachment length matches
// the number of decoded bytes; ok is false when the field was absent or invalid.
func CheckAttachmentLength(declared int64, ok bool, actual int64) error {
	if !ok {
		return fmt.Errorf("%w: length missing or invalid type", ErrInvalidAttachment)
	}

	if actual != declared {
		return fmt.Errorf("%w: length mismatch: declared %d, actual %d", ErrInvalidAttachment, declared, actual)
	}

	return nil
}
