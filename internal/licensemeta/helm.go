// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/pkg/helm"
)

// helmLicenseAnnotation is the only standard declaration of a chart's license:
// Chart.yaml has no license field.
const helmLicenseAnnotation = "artifacthub.io/license"

// HelmHooks reads the licenses of helm from the chart's Chart.yaml with the
// parser the upload uses, not through the manifest package: a stored chart must
// yield exactly the license its upload was judged on.
var HelmHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	Expected: expectSupported,
	Read:     readChart,
}

// readChart reads the artifacthub.io/license annotation of a chart archive.
func readChart(_ context.Context, open func() (io.ReadCloser, error)) (Result, error) {
	rc, err := open()
	if err != nil {
		return Result{}, fmt.Errorf("open chart: %w", err)
	}

	defer func() { _ = rc.Close() }()

	chart, err := helm.ParseChart(rc)

	switch {
	case errors.Is(err, helm.ErrChartTooLarge), errors.Is(err, helm.ErrChartYAMLTooLarge):
		return Result{}, fmt.Errorf("%w: %w", ErrUnsafe, err)
	case errors.Is(err, helm.ErrChartYAMLNotFound):
		return Result{}, fmt.Errorf("%w: %w", ErrNoMetadata, err)
	case err != nil:
		return Result{}, fmt.Errorf("read chart: %w", err)
	}

	annotations, _ := chart["annotations"].(map[string]string)

	res := Result{Source: "Chart.yaml"}
	if l := strings.TrimSpace(annotations[helmLicenseAnnotation]); l != "" {
		res.Licenses = []string{l}
	}

	return res, nil
}
