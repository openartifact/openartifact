#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# pypi: build a wheel, twine upload (legacy upload API), pip install it back
# from the PEP 503 simple index. Image: PYPI_IMAGE (default python:3-slim).
# Usage: OA_TOKEN=... scripts/smoke/pypi.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init pypi python:3-slim "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
pip install -q build twine setuptools wheel >/dev/null 2>&1
pip --version
mkdir -p /tmp/p/src && cd /tmp/p
printf "[build-system]\nrequires=[\"setuptools\"]\nbuild-backend=\"setuptools.build_meta\"\n[project]\nname=\"%s\"\nversion=\"%s\"\n" "$PKG" "$VERSION" > pyproject.toml
printf "[tool.setuptools]\npy-modules=[\"%s\"]\n" "$PKG" >> pyproject.toml
echo "VALUE = \"$PKG\"" > $PKG.py
python -m build --wheel --no-isolation >/dev/null 2>&1
TWINE_USERNAME=smoke TWINE_PASSWORD=$OA_TOKEN twine upload --non-interactive \
  --repository-url "$OA_URL/repos/$REPO/legacy/" dist/*.whl >/dev/null
hp="${OA_URL#*://}"; scheme="${OA_URL%%://*}"
pip install -q --index-url "$scheme://smoke:$OA_TOKEN@$hp/repos/$REPO/simple/" "$PKG==$VERSION" >/dev/null
[ "$(python -c "import $PKG; print($PKG.VALUE)")" = "$PKG" ]
SCRIPT
)"
