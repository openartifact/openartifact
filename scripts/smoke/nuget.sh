#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# nuget: dotnet pack a class library, dotnet nuget push to the v3 feed, then a
# consumer project restores it with the repository as its only source.
# Image: NUGET_IMAGE (default mcr.microsoft.com/dotnet/sdk:latest).
# Usage: OA_TOKEN=... scripts/smoke/nuget.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init nuget mcr.microsoft.com/dotnet/sdk:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
export DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1
echo "dotnet $(dotnet --version)"
feed="$OA_URL/repos/$REPO/v3/index.json"
tfm="net$(dotnet --version | cut -d. -f1).0" # the SDK's own target: nothing to restore from nuget.org
cat > /root/oa.config <<EOF
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="oa" value="$feed" allowInsecureConnections="true" />
  </packageSources>
  <packageSourceCredentials>
    <oa>
      <add key="Username" value="smoke" />
      <add key="ClearTextPassword" value="$OA_TOKEN" />
    </oa>
  </packageSourceCredentials>
</configuration>
EOF
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p && cd /tmp/p
  cat > "$PKG.csproj" <<EOF
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>$tfm</TargetFramework>
    <PackageId>$PKG</PackageId>
    <Version>$VERSION</Version>
    <Authors>smoke</Authors>
    <Description>smoke test package</Description>
  </PropertyGroup>
  <ItemGroup><None Include="marker.txt" Pack="true" PackagePath="/" /></ItemGroup>
</Project>
EOF
  echo "marker $PKG" > marker.txt
  echo "namespace Smoke { public static class Marker { public const string Value = \"$PKG\"; } }" > Marker.cs
  dotnet pack -c Release -o /tmp/out -v q
  dotnet nuget push "/tmp/out/$PKG.$VERSION.nupkg" --source oa --api-key smoke --configfile /root/oa.config
}
[ "$SMOKE_MODE" = publish ] || {
  # fresh nuget state: its own package folder and HTTP cache
  export NUGET_HTTP_CACHE_PATH=/tmp/nuget-http-install
  mkdir -p /tmp/c && cd /tmp/c
  cp /root/oa.config nuget.config
  cat > c.csproj <<EOF
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>$tfm</TargetFramework></PropertyGroup>
  <ItemGroup><PackageReference Include="$PKG" Version="$VERSION" /></ItemGroup>
</Project>
EOF
  dotnet restore --packages /tmp/pkgs --no-cache -v q
  dll="/tmp/pkgs/$PKG/$VERSION/lib/$tfm/$PKG.dll"
  ls "$dll"
  [ "$(cat "/tmp/pkgs/$PKG/$VERSION/marker.txt")" = "marker $PKG" ]
}
SCRIPT
)"
