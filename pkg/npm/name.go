// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrInvalidPackageName = errors.New("invalid npm package name")

var urlSafeRe = regexp.MustCompile(`^[a-z0-9\-_.~]+$`)

//nolint:gochecknoglobals // immutable Node.js core module list
var coreModules = map[string]bool{
	"http": true, "https": true, "fs": true, "path": true, "os": true,
	"url": true, "util": true, "crypto": true, "net": true, "tls": true,
	"events": true, "stream": true, "buffer": true, "process": true,
	"child_process": true, "cluster": true, "dgram": true, "dns": true,
	"domain": true, "module": true, "punycode": true, "querystring": true,
	"readline": true, "repl": true, "string_decoder": true, "tty": true,
	"vm": true, "zlib": true, "assert": true, "async_hooks": true,
	"console": true, "inspector": true, "perf_hooks": true, "timers": true,
	"trace_events": true, "v8": true, "worker_threads": true,
}

func ValidatePackageName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: must not be empty", ErrInvalidPackageName)
	}

	if len(name) > 214 {
		return fmt.Errorf("%w: exceeds 214 characters", ErrInvalidPackageName)
	}

	if strings.HasPrefix(name, "@") {
		return validateScoped(name)
	}

	return validateSegment(name, true)
}

func validateScoped(name string) error {
	rest := name[1:]

	before, after, ok := strings.Cut(rest, "/")
	if !ok {
		return fmt.Errorf("%w: scoped name missing /", ErrInvalidPackageName)
	}

	scope := before
	pkg := after

	if scope == "" {
		return fmt.Errorf("%w: scope must not be empty", ErrInvalidPackageName)
	}

	if pkg == "" {
		return fmt.Errorf("%w: package part after scope must not be empty", ErrInvalidPackageName)
	}

	if strings.Contains(pkg, "/") {
		return fmt.Errorf("%w: scoped name must have exactly one /", ErrInvalidPackageName)
	}

	if err := validateSegment(scope, false); err != nil {
		return err
	}

	return validateSegment(pkg, true)
}

func validateSegment(seg string, checkCore bool) error {
	if strings.HasPrefix(seg, ".") {
		return fmt.Errorf("%w: must not start with \".\"", ErrInvalidPackageName)
	}

	if strings.HasPrefix(seg, "_") {
		return fmt.Errorf("%w: must not start with _", ErrInvalidPackageName)
	}

	for _, ch := range seg {
		if ch >= 'A' && ch <= 'Z' {
			return fmt.Errorf("%w: must not contain uppercase characters", ErrInvalidPackageName)
		}
	}

	if !urlSafeRe.MatchString(seg) {
		return fmt.Errorf("%w: contains URL-unsafe characters", ErrInvalidPackageName)
	}

	if checkCore && coreModules[seg] {
		return fmt.Errorf("%w: %s is a Node.js core module name", ErrInvalidPackageName, seg)
	}

	return nil
}
