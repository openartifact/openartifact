// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"errors"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
)

// ErrInvalidForm reports a multipart/form-data body that cannot be read: not
// multipart, truncated or malformed. A body over the request's byte cap is not
// reported with it alone: the error also matches *http.MaxBytesError.
var ErrInvalidForm = errors.New("invalid multipart form")

// ErrFormFieldsTooLarge reports that the text fields of a form together exceed
// FormSpec.MaxFields.
var ErrFormFieldsTooLarge = errors.New("form fields too large")

// ErrNoFormFile reports a form without its file part.
var ErrNoFormFile = errors.New("form has no file part")

// DefaultMaxFormFields caps the text fields of a form when FormSpec.MaxFields
// is zero: the limit net/http's own form parser applies to non-file data.
const DefaultMaxFormFields = 10 << 20

// FormSpec describes the multipart upload ReadForm reads.
type FormSpec struct {
	// FileField is the name of the file part.
	FileField string
	// MaxFile is the most bytes the file part may hold; more fails with
	// ErrContentTooLarge.
	MaxFile int64
	// MaxFields is the most bytes all text fields may hold together, 0 for
	// DefaultMaxFormFields.
	MaxFields int64
	// Hashes also receive every byte of the file part (SHA-256 is always
	// computed).
	Hashes []hash.Hash
}

// Form is a multipart upload read in one streaming pass: the text fields are
// held in memory and the file part is a Spool, written once to its temp file
// (or kept in memory when small) while it is hashed. net/http's
// ParseMultipartForm would first buffer the part in memory up to its limit and
// then in its own temp file, and removes that file only for the original
// *http.Request: a request copied by r.WithContext (every routed request is)
// leaked a full-size file per upload. A Form owns no file but the spool, which
// Close removes.
type Form struct {
	// File is the file part; nil when the form has none and ReadForm returned
	// ErrNoFormFile.
	File *Spool
	// Filename is the file part's client-supplied name.
	Filename string
	// Header is the file part's MIME header (Content-Type, Content-Transfer-Encoding).
	Header textproto.MIMEHeader

	values url.Values
	query  url.Values
}

// Value returns the first value of the text field name, falling back to the
// URL query like http.Request.FormValue.
func (f *Form) Value(name string) string {
	if v := f.values[name]; len(v) > 0 {
		return v[0]
	}

	return f.query.Get(name)
}

// Values returns every value of the text field name: the form's, then the URL
// query's (http.Request.Form order).
func (f *Form) Values(name string) []string {
	return append(append([]string(nil), f.values[name]...), f.query[name]...)
}

// Close releases the file part. Safe on nil.
func (f *Form) Close() {
	if f != nil {
		f.File.Close()
	}
}

// ReadForm reads the multipart/form-data body of r, wrapped by the caller in
// http.MaxBytesReader to cap the whole body. A body longer than the cap fails
// with an error matching *http.MaxBytesError, a file part longer than
// spec.MaxFile with ErrContentTooLarge, too many field bytes with
// ErrFormFieldsTooLarge, a form without the file part with ErrNoFormFile and
// anything unreadable with ErrInvalidForm. The caller closes the Form it gets
// on success; on ErrNoFormFile it is returned too, so the fields can be read.
func ReadForm(r *http.Request, spec FormSpec) (*Form, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidForm, err)
	}

	form := &Form{values: url.Values{}, query: r.URL.Query()}

	fieldBudget := spec.MaxFields
	if fieldBudget <= 0 {
		fieldBudget = DefaultMaxFormFields
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			form.Close()

			return nil, fmt.Errorf("%w: %w", ErrInvalidForm, err)
		}

		if err := form.readPart(part, spec, &fieldBudget); err != nil {
			form.Close()

			return nil, err
		}
	}

	if form.File == nil {
		return form, ErrNoFormFile
	}

	return form, nil
}

// readPart stores one part: the first file part named spec.FileField is
// spooled, every other file part is skipped and a text part goes to the fields.
func (f *Form) readPart(part *multipart.Part, spec FormSpec, fieldBudget *int64) error {
	defer func() { _ = part.Close() }()

	if part.FormName() == spec.FileField && part.FileName() != "" && f.File == nil {
		spool, err := SpoolLimited(part, spec.MaxFile, spec.Hashes...)
		if err != nil {
			return wrapFormRead(err)
		}

		f.File, f.Filename, f.Header = spool, part.FileName(), part.Header

		return nil
	}

	if part.FileName() != "" {
		if _, err := io.Copy(io.Discard, part); err != nil {
			return wrapFormRead(err)
		}

		return nil
	}

	value, err := io.ReadAll(io.LimitReader(part, *fieldBudget+1))
	if err != nil {
		return wrapFormRead(err)
	}

	if int64(len(value)) > *fieldBudget {
		return ErrFormFieldsTooLarge
	}

	*fieldBudget -= int64(len(value))
	f.values.Add(part.FormName(), string(value))

	return nil
}

// IsTooLarge reports whether err means the body, the file part or the fields
// of a form exceeded a limit (the 413 cases of ReadForm).
func IsTooLarge(err error) bool {
	if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
		return true
	}

	return errors.Is(err, ErrContentTooLarge) || errors.Is(err, ErrFormFieldsTooLarge)
}

// wrapFormRead marks a read failure as a form failure while keeping the
// limits (ErrContentTooLarge, *http.MaxBytesError) matchable.
func wrapFormRead(err error) error {
	if errors.Is(err, ErrContentTooLarge) {
		return err
	}

	return fmt.Errorf("%w: %w", ErrInvalidForm, err)
}
