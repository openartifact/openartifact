#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# cocoapods: a proxy repository of the CocoaPods CDN (no hosted or group
# repositories exist for this format). `pod repo add-cdn` registers it, then
# `pod install` of a generated Xcode project fetches two pods through it: one
# with an http source (Alamofire) and one with a GitHub tag source (SnapKit).
# The install is repeated in a fresh CocoaPods home, now served from the proxy
# cache. The publish phase is the first install (it fills the proxy cache), the
# install phase the second one. Credentials go in ~/.netrc, as for a real client.
# Image: COCOAPODS_IMAGE (default ruby:3.3; the newest cocoapods gem is installed).
# Needs Internet access for the server and the container (cdn.cocoapods.org, github.com).
# Usage: OA_TOKEN=... scripts/smoke/cocoapods.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
SMOKE_REPO_BODY='{"repo_type":"proxy"}'
smoke_init cocoapods docker.io/library/ruby:3.3 "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
export LANG=C.UTF-8 COCOAPODS_ALLOW_ROOT=1
apt-get update -qq >/dev/null && apt-get install -y -qq rsync >/dev/null
gem install --no-document cocoapods xcodeproj >/dev/null
pod --version
host=${OA_URL#*://}
host=${host%%/*}
host=${host%:*}
umask 077
printf 'machine %s login oa password %s\n' "$host" "$OA_TOKEN" > "$HOME/.netrc"
mkdir -p /tmp/app
cd /tmp/app
ruby -rxcodeproj -e 'p = Xcodeproj::Project.new("App.xcodeproj"); p.new_target(:application, "App", :ios, "15.0"); p.save'
cat > Podfile <<PODFILE
source '$OA_URL/repos/$REPO'
platform :ios, '15.0'
target 'App' do
  pod 'Alamofire', '5.9.1'
  pod 'SnapKit', '5.7.1'
end
PODFILE
install_pods() { # install_pods HOME: pod install with a fresh CocoaPods home, prints the lock checksums
  rm -rf Pods Podfile.lock
  CP_HOME_DIR="$1" pod repo add-cdn smoke "$OA_URL/repos/$REPO" >/dev/null
  CP_HOME_DIR="$1" pod spec cat Alamofire --version=5.9.1 | grep -q "$OA_URL/repos/$REPO/dl/"
  CP_HOME_DIR="$1" pod install >/dev/null
  [ -d Pods/Alamofire/Source ] && [ -d Pods/SnapKit/Sources ]
  grep -A3 'SPEC CHECKSUMS' Podfile.lock
}
[ "$SMOKE_MODE" = install ] || { install_pods /tmp/home1 > /tmp/first.sum; }
[ "$SMOKE_MODE" = publish ] || {
  install_pods /tmp/home2 > /tmp/second.sum
  [ "$SMOKE_MODE" = install ] || cmp /tmp/first.sum /tmp/second.sum
}
SCRIPT
)"
