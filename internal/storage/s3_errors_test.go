// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func httpStatusError(status int) error {
	return &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
			Err:      errors.New("status"),
		},
	}
}

// S3-compatible backends report the same condition as a typed error, an API
// error code or only a bare HTTP status; each spelling must be recognised,
// including when wrapped.
func TestS3ErrorClassification(t *testing.T) {
	t.Parallel()

	notFound := &types.NotFound{}
	noKey := &types.NoSuchKey{}
	noUpload := &types.NoSuchUpload{}
	other := errors.New("connection reset")

	tests := []struct {
		name                                               string
		err                                                error
		isNotFound, isNoUpload, isNotImpl, isPrecondFailed bool
	}{
		{"nil", nil, false, false, false, false},
		{"unrelated", other, false, false, false, false},
		{"head not found", notFound, true, false, false, false},
		{"no such key", noKey, true, false, false, false},
		{"wrapped no such key", fmt.Errorf("get: %w", noKey), true, false, false, false},
		{"no such upload", noUpload, false, true, false, false},
		{"no such upload code", &smithy.GenericAPIError{Code: "NoSuchUpload"}, false, true, false, false},
		{"not implemented code", &smithy.GenericAPIError{Code: "NotImplemented"}, false, false, true, false},
		{"not implemented status", httpStatusError(http.StatusNotImplemented), false, false, true, false},
		{"precondition code", &smithy.GenericAPIError{Code: "PreconditionFailed"}, false, false, false, true},
		{"precondition status", httpStatusError(http.StatusPreconditionFailed), false, false, false, true},
		{"other status", httpStatusError(http.StatusInternalServerError), false, false, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isNotFound(tc.err); got != tc.isNotFound {
				t.Errorf("isNotFound = %v", got)
			}

			if got := isNoSuchUpload(tc.err); got != tc.isNoUpload {
				t.Errorf("isNoSuchUpload = %v", got)
			}

			if got := isNotImplemented(tc.err); got != tc.isNotImpl {
				t.Errorf("isNotImplemented = %v", got)
			}

			if got := isPreconditionFailed(tc.err); got != tc.isPrecondFailed {
				t.Errorf("isPreconditionFailed = %v", got)
			}
		})
	}
}

// A failed promote copy is reported with the staging sentinels the upload
// flow branches on; anything else keeps its cause and names both keys.
func TestMapCopyError(t *testing.T) {
	t.Parallel()

	boom := errors.New("backend down")

	tests := []struct {
		name    string
		err     error
		want    error
		wrapped bool // the cause is kept and both keys are named
	}{
		{"source missing", &types.NotFound{}, ErrStagingNotFound, false},
		{"source key missing", &types.NoSuchKey{}, ErrStagingNotFound, false},
		{"source changed", httpStatusError(http.StatusPreconditionFailed), ErrStagingChanged, false},
		{"source changed by code", &smithy.GenericAPIError{Code: "PreconditionFailed"}, ErrStagingChanged, false},
		{"other failure", boom, boom, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := mapCopyError(tc.err, "staging/a", "blobs/b")
			if !errors.Is(got, tc.want) {
				t.Fatalf("mapCopyError = %v, want %v", got, tc.want)
			}

			if tc.wrapped && !containsAll(got.Error(), "staging/a", "blobs/b") {
				t.Fatalf("error %q does not name both keys", got)
			}

			if !tc.wrapped && errors.Is(got, ErrStagingNotFound) == errors.Is(got, ErrStagingChanged) {
				t.Fatalf("mapCopyError = %v maps to both or neither sentinel", got)
			}
		})
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}

	return true
}
