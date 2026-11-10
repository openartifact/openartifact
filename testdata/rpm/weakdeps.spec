# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Fixture for pkg/rpm weak dependency tests (weakdeps.rpm). Rebuild with:
#   rpmbuild --define "_topdir $PWD" --define "_rpmdir $PWD/out" \
#     --define "_build_id_links none" -bb weakdeps.spec
Name: oa-weakdep-test
Version: 1.0
Release: 1
Epoch: 1
Summary: weak dep fixture
License: MIT
BuildArch: noarch
Requires: oa-required >= 2:1.5-3
Requires: shadow-utils
Recommends: oa-recommended
Suggests: oa-suggested >= 1.0
Supplements: oa-supplemented
Enhances: oa-enhanced
Conflicts: oa-conflicting < 2.0-1
Obsoletes: oa-old < 1.0
%description
fixture
%files
