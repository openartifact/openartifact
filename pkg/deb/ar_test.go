// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"testing"
)

func TestParseARHeader(t *testing.T) {
	// Create a valid AR header
	headerBuf := make([]byte, 60)
	copy(headerBuf[0:16], "test.txt        ")
	copy(headerBuf[16:28], "1234567890  ")
	copy(headerBuf[28:34], "0     ")
	copy(headerBuf[34:40], "0     ")
	copy(headerBuf[40:48], "100644  ")
	copy(headerBuf[48:58], "1024      ")
	headerBuf[58] = '`'
	headerBuf[59] = '\n'

	header, err := parseARHeader(headerBuf)
	if err != nil {
		t.Fatalf("parseARHeader() error = %v", err)
	}

	if header.Name != "test.txt" {
		t.Errorf("Name = %v, want test.txt", header.Name)
	}

	if header.Size != 1024 {
		t.Errorf("Size = %v, want 1024", header.Size)
	}
}

func TestParseARHeaderInvalid(t *testing.T) {
	tests := []struct {
		name    string
		setup   func([]byte)
		wantErr bool
	}{
		{
			name: "invalid ending marker",
			setup: func(buf []byte) {
				buf[58] = 'X'
				buf[59] = 'Y'
			},
			wantErr: true,
		},
		{
			name: "invalid size",
			setup: func(buf []byte) {
				copy(buf[48:58], "invalid   ")
				buf[58] = '`'
				buf[59] = '\n'
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headerBuf := make([]byte, 60)
			copy(headerBuf[0:16], "test.txt        ")
			copy(headerBuf[16:28], "1234567890  ")
			copy(headerBuf[28:34], "0     ")
			copy(headerBuf[34:40], "0     ")
			copy(headerBuf[40:48], "100644  ")
			copy(headerBuf[48:58], "1024      ")
			headerBuf[58] = '`'
			headerBuf[59] = '\n'

			tt.setup(headerBuf)

			_, err := parseARHeader(headerBuf)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseARHeader() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
