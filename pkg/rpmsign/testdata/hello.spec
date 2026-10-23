# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

Name: oa-hello
Version: 1.0.0
Release: 1
Summary: rpmsign test package
License: MIT
BuildArch: noarch
%description
Test package for pkg/rpmsign. Rebuild with:
rpmbuild --define "_rpmformat 4" --define "_topdir $PWD/rb" --define "_buildhost localhost" --define "source_date_epoch_from_changelog 0" --define "_binary_payload w9.gzdio" --define "use_source_date_epoch_as_buildtime 1" -bb hello.spec
%install
mkdir -p %{buildroot}/usr/share/hello
echo hi > %{buildroot}/usr/share/hello/hello.txt
%files
/usr/share/hello/hello.txt
