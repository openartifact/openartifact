// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"errors"
	"testing"
)

// normalizeCases were produced by Composer 2.10's VersionParser::normalize (an
// empty result is a rejected version); the first rows are the successful cases
// of Composer's own VersionParserTest.
func normalizeCases() [][2]string {
	return [][2]string{
		{"1.0.0", "1.0.0.0"},
		{"1.2.3.4", "1.2.3.4"},
		{"1.0.0RC1dev", "1.0.0.0-RC1-dev"},
		{"1.0.0-rC15-dev", "1.0.0.0-RC15-dev"},
		{"1.0.0.RC.15-dev", "1.0.0.0-RC15-dev"},
		{"1.0.0-rc1", "1.0.0.0-RC1"},
		{"1.0.0.pl3-dev", "1.0.0.0-patch3-dev"},
		{"1.0-dev", "1.0.0.0-dev"},
		{"0", "0.0.0.0"},
		{"10.4.13-beta", "10.4.13.0-beta"},
		{"10.4.13beta2", "10.4.13.0-beta2"},
		{"10.4.13-beta.2", "10.4.13.0-beta2"},
		{"10.4.13-b", "10.4.13.0-beta"},
		{"10.4.13-b5", "10.4.13.0-beta5"},
		{"v1.0.0", "1.0.0.0"},
		{"V1.0.0", "1.0.0.0"},
		{"2010-01", "2010.01"},
		{"2010.01", "2010.01.0.0"},
		{"2010.01.02", "2010.01.02.0"},
		{"2010-01-02", "2010.01.02"},
		{"2012.06.07", "2012.06.07.0"},
		{"2010-01-02.5", "2010.01.02.5"},
		{"2010.1.555", "2010.1.555.0"},
		{"20100102-203040", "20100102.203040"},
		{"20100102203040-10", "20100102203040.10"},
		{"20100102-203040-p1", "20100102.203040-patch1"},
		{"dev-master", "dev-master"},
		{"master", "dev-master"},
		{"trunk", "dev-trunk"},
		{"default", "dev-default"},
		{"dev-trunk", "dev-trunk"},
		{"1.x-dev", "1.9999999.9999999.9999999-dev"},
		{"1.0.x-dev", "1.0.9999999.9999999-dev"},
		{"1.0.x", ""},
		{"v1.x-dev", "1.9999999.9999999.9999999-dev"},
		{"1.*-dev", "1.9999999.9999999.9999999-dev"},
		{"dev-feature-foo", "dev-feature-foo"},
		{"DEV-FOOBAR", "dev-FOOBAR"},
		{"dev-feature/foo", "dev-feature/foo"},
		{"dev-master as 1.0.0", "dev-master"},
		{"1.0.0+foo", "1.0.0.0"},
		{"1.0.0-beta.5+foo", "1.0.0.0-beta5"},
		{"1.0.0@dev", "1.0.0.0"},
		{"1.0.0@stable", "1.0.0.0"},
		{"1.0.0@RC", "1.0.0.0"},
		{"1.0.0-stable", "1.0.0.0"},
		{"1.0.0-STABLE", "1.0.0.0-stable"},
		{"1.0.0-alpha3", "1.0.0.0-alpha3"},
		{"1.0.0-a3", "1.0.0.0-alpha3"},
		{"1.0.0-patch2", "1.0.0.0-patch2"},
		{"1.0.0-p", "1.0.0.0-patch"},
		{"1.0.0-pl", "1.0.0.0-patch"},
		{"1.0.0.0", "1.0.0.0"},
		{"1.0.0.0.0", ""},
		{"1.0.0.", "1.0.0.0"},
		{".1.0", ""},
		{"1.0-", "1.0.0.0"},
		{"foo", ""},
		{"foo-dev", ""},
		{"1.0.0-foo", ""},
		{"1.2.3-beta1-dev", "1.2.3.0-beta1-dev"},
		{"1.2.3.4-dev", "1.2.3.4-dev"},
		{"1-dev", "1.0.0.0-dev"},
		{"1.x", ""},
		{"x.y", ""},
		{"1234567", "1234567"},
		{"123456", "123456"},
		{"12345", "12345.0.0.0"},
		{"99999.99999.99999.99999", "99999.99999.99999.99999"},
		{"100000.0", "100000.0"},
		{"1.0.0-RC1.2", "1.0.0.0-RC1.2"},
		{"1.0.0-RC.1.2", "1.0.0.0-RC1.2"},
		{"1.0.0rc", "1.0.0.0-RC"},
		{"1.0.0RC-dev", "1.0.0.0-RC-dev"},
		{"2024-02-30", "2024.02.30"},
		{"2010.1", "2010.1.0.0"},
		{"2010-1", ""},
		{"2010.01.02.03.04.05.06", "2010.01.02.03.04.05.06"},
		{"dev-", "dev-"},
		{"dev-main", "dev-main"},
		{"Dev-Main", "dev-Main"},
		{"1.0.0--dev", "1.0.0.0-dev"},
		{"1.0-patch", "1.0.0.0-patch"},
		{"1.2.3-beta-2", "1.2.3.0-beta2"},
		{"1.2.3_beta", "1.2.3.0-beta"},
		{"1.2.3_dev", "1.2.3.0-dev"},
		{"v2", "2.0.0.0"},
		{"2", "2.0.0.0"},
		{"2.0", "2.0.0.0"},
		{"3.0.0-rc.1", "3.0.0.0-RC1"},
		{"1.0.0.dev", "1.0.0.0-dev"},
		{"1.0.0.DEV", "1.0.0.0-dev"},
		{"1.0.0 ", "1.0.0.0"},
		{" 1.0.0", "1.0.0.0"},
		{"1.0.0+", ""},
		{"1.0.0+a b", ""},
	}
}

func TestNormalizeMatchesComposer(t *testing.T) {
	t.Parallel()

	for _, c := range normalizeCases() {
		got, err := Normalize(c[0])
		if c[1] == "" {
			if !errors.Is(err, ErrInvalidVersion) {
				t.Errorf("Normalize(%q) = %q, %v; want ErrInvalidVersion", c[0], got, err)
			}

			continue
		}

		if err != nil || got != c[1] {
			t.Errorf("Normalize(%q) = %q, %v; want %q", c[0], got, err, c[1])
		}
	}
}

func TestIsDev(t *testing.T) {
	t.Parallel()

	for v, want := range map[string]bool{
		"dev-main": true, "1.9999999.9999999.9999999-dev": true, "1.0.0.0-dev": true,
		"1.0.0.0": false, "1.0.0.0-RC1": false, "2.0.0.0-beta3": false,
	} {
		if got := IsDev(v); got != want {
			t.Errorf("IsDev(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestNormalizePublished(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"1.2.3", "1.2.3.0", true},
		{"v1.2.3-beta1", "1.2.3.0-beta1", true},
		{"dev-main", "dev-main", true},
		{"dev-feature/x", "dev-feature/x", true},
		{"master", "dev-master", true},
		{"1.x-dev", "1.9999999.9999999.9999999-dev", true},
		{"1.0.0+build.5", "1.0.0.0", true},
		{"dev-", "", false},
		{"", "", false},
		{" 1.0.0", "", false},
		{"1.0.0@dev", "", false},
		{"dev-main as 1.0.0", "", false},
		{"1.0 0", "", false},
		{"1.0.0\n", "", false},
		{"nonsense", "", false},
		{"../1.0.0", "", false},
	} {
		got, err := NormalizePublished(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizePublished(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}
