// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxSecretBytes bounds a secret read from a file.
const maxSecretBytes = 1 << 20

// SecretResolver turns SecretRefs into values on the machine running oa.
// The zero value reads the process environment and the file system.
type SecretResolver struct {
	// LookupEnv defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// ReadFile defaults to a bounded os.ReadFile.
	ReadFile func(string) ([]byte, error)
}

// ErrSecretUnresolved is wrapped by every resolution failure.
var ErrSecretUnresolved = errors.New("secret cannot be resolved")

// Resolve returns the secret ref points at. Empty secrets are an error, a
// trailing newline of a file is stripped.
func (r SecretResolver) Resolve(ref SecretRef) (string, error) {
	if ref.Env != "" {
		lookup := r.LookupEnv
		if lookup == nil {
			lookup = os.LookupEnv
		}

		v, ok := lookup(ref.Env)
		if !ok {
			return "", fmt.Errorf("%w: environment variable %s is not set", ErrSecretUnresolved, ref.Env)
		}

		if v == "" {
			return "", fmt.Errorf("%w: environment variable %s is empty", ErrSecretUnresolved, ref.Env)
		}

		return v, nil
	}

	read := r.ReadFile
	if read == nil {
		read = readSecretFile
	}

	data, err := read(ref.File)
	if err != nil {
		return "", fmt.Errorf("%w: file %s: %w", ErrSecretUnresolved, ref.File, err)
	}

	v := strings.TrimRight(string(data), "\r\n")
	if v == "" {
		return "", fmt.Errorf("%w: file %s is empty", ErrSecretUnresolved, ref.File)
	}

	return v, nil
}

func readSecretFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxSecretBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if len(data) > maxSecretBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxSecretBytes) //nolint:err113 // user-facing message
	}

	return data, nil
}

// resolveValue returns v with every SecretRef replaced by its value.
func (r SecretResolver) resolveValue(v any) (any, error) {
	switch t := v.(type) {
	case SecretRef:
		return r.Resolve(t)
	case map[string]any:
		out := make(map[string]any, len(t))

		for k, e := range t {
			rv, err := r.resolveValue(e)
			if err != nil {
				return nil, err
			}

			out[k] = rv
		}

		return out, nil
	case []any:
		out := make([]any, len(t))

		for i, e := range t {
			rv, err := r.resolveValue(e)
			if err != nil {
				return nil, err
			}

			out[i] = rv
		}

		return out, nil
	}

	return v, nil
}

// resolveObject returns a copy of o whose spec has every SecretRef resolved.
func (r SecretResolver) resolveObject(o *Object) (*Object, error) {
	spec, err := r.resolveValue(o.Spec)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Ident(o.Kind, o.Name), err)
	}

	cp := *o
	cp.Spec, _ = spec.(map[string]any)

	return &cp, nil
}

// resolveChanges returns changes with secret values resolved.
func (r SecretResolver) resolveChanges(ident string, changes []Change) ([]Change, error) {
	out := make([]Change, len(changes))

	for i, c := range changes {
		out[i] = c

		if !c.Secret {
			continue
		}

		ref, ok := c.New.(SecretRef)
		if !ok {
			continue
		}

		v, err := r.Resolve(ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", ident, c.Path, err)
		}

		out[i].New = v
	}

	return out, nil
}
