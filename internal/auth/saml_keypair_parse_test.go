// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"reflect"
	"testing"
	"time"
)

func certPEMFor(t *testing.T, key *rsa.PrivateKey, notBefore, notAfter time.Time) []byte {
	t.Helper()

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "test-sp"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestParseSAMLKeypair(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	pkcs8Der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Der})

	ecDer, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}

	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDer})

	valid := certPEMFor(t, key, now.Add(-time.Hour), now.Add(time.Hour))
	expired := certPEMFor(t, key, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	otherCert := certPEMFor(t, other, now.Add(-time.Hour), now.Add(time.Hour))

	tests := []struct {
		name    string
		key     []byte
		cert    []byte
		wantErr bool
	}{
		{name: "pkcs1 key", key: pkcs1, cert: valid},
		{name: "pkcs8 key", key: pkcs8, cert: valid},
		// Only the keypair match is validated; an expired certificate is loaded as is.
		{name: "expired certificate is loaded", key: pkcs1, cert: expired},
		{name: "key not PEM", key: []byte("nope"), cert: valid, wantErr: true},
		{name: "cert not PEM", key: pkcs1, cert: []byte("nope"), wantErr: true},
		{name: "empty inputs", wantErr: true},
		{name: "key and cert swapped", key: valid, cert: pkcs1, wantErr: true},
		{name: "key does not match cert", key: pkcs1, cert: otherCert, wantErr: true},
		{name: "non-RSA key", key: ecPEM, cert: valid, wantErr: true},
		{
			name:    "corrupt key body",
			key:     pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("xx")}),
			cert:    valid,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotCert, err := parseSAMLKeypair(tt.key, tt.cert)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}

				if gotKey != nil || gotCert != nil {
					t.Error("no key or cert may be returned on error")
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !gotKey.Equal(key) || gotCert.Subject.CommonName != "test-sp" {
				t.Error("parsed keypair does not match the input")
			}
		})
	}
}

func TestGroupsFromClaims(t *testing.T) {
	tests := []struct {
		name        string
		claims      map[string]any
		key         string
		want        []string
		wantPresent bool
	}{
		{
			name:        "string array",
			claims:      map[string]any{"groups": []string{"a", "b"}},
			key:         "groups",
			want:        []string{"a", "b"},
			wantPresent: true,
		},
		{name: "any array", claims: map[string]any{"groups": []any{"a", "b"}}, key: "groups", want: []string{"a", "b"}, wantPresent: true},
		{
			name:        "non-string items dropped",
			claims:      map[string]any{"groups": []any{"a", 3, nil, "b"}},
			key:         "groups",
			want:        []string{"a", "b"},
			wantPresent: true,
		},
		{name: "empty array is present", claims: map[string]any{"groups": []any{}}, key: "groups", want: []string{}, wantPresent: true},
		{name: "bare string", claims: map[string]any{"groups": "solo"}, key: "groups", want: []string{"solo"}, wantPresent: true},
		{name: "empty string is empty list", claims: map[string]any{"groups": ""}, key: "groups", want: []string{}, wantPresent: true},
		{
			name:        "custom claim key",
			claims:      map[string]any{"roles": []any{"r"}, "groups": []any{"g"}},
			key:         "roles",
			want:        []string{"r"},
			wantPresent: true,
		},
		{name: "missing", claims: map[string]any{}, key: "groups"},
		{name: "null", claims: map[string]any{"groups": nil}, key: "groups"},
		{name: "number", claims: map[string]any{"groups": 5.0}, key: "groups"},
		{name: "bool", claims: map[string]any{"groups": true}, key: "groups"},
		{
			name:   "nested object is not dotted lookup",
			claims: map[string]any{"realm": map[string]any{"groups": []any{"a"}}},
			key:    "realm.groups",
		},
		{name: "object value", claims: map[string]any{"groups": map[string]any{"a": "b"}}, key: "groups"},
		{name: "nil claims", claims: nil, key: "groups"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present := groupsFromClaims(tt.claims, tt.key)
			if present != tt.wantPresent {
				t.Fatalf("present = %v, want %v", present, tt.wantPresent)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("groups = %#v, want %#v", got, tt.want)
			}
		})
	}
}
