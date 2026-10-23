// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

// specialFixtures covers the formats with their own promoter.
func specialFixtures() []fixture {
	return []fixture{debFixture(), rpmFixture(), dockerFixture(), npmFixture(), goFixture(), conanFixture()}
}
