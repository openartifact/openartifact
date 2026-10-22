# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Fixture for pkg/rpm size tests. Rebuild (format 4 or 6) with:
#   rpmbuild --define "_topdir $PWD" --define "_rpmdir $PWD/out" \
#     --define "_rpmformat 4" --define "_build_id_links none" -bb oa-sizes.spec
Name: oa-sizes
Version: 1.2.3
Release: 4
Epoch: 2
Summary: Fixture for installed/archive size parsing
License: MIT
BuildArch: noarch
Requires: bash
%description
Fixture for installed/archive size parsing.
%install
mkdir -p %{buildroot}/usr/share/oa-sizes
head -c 100000 /dev/zero > %{buildroot}/usr/share/oa-sizes/zeros.bin
echo hi > %{buildroot}/usr/share/oa-sizes/hello.txt
ln -s hello.txt %{buildroot}/usr/share/oa-sizes/link
%files
/usr/share/oa-sizes
