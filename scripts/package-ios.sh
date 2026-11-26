#!/usr/bin/env bash
# Packages the CanalPeer iOS app as an unsigned .ipa: SideStore signs it on
# each viewer's device with their own Apple ID, so no Apple account of ours
# is in it.
#   package-ios.sh release  -> dist/CanalPeer-<v>-ios.ipa, privacy-checked
#   package-ios.sh publish  -> upload that .ipa to the canalpeer bucket (build it
#                              first: its size and sha256 go in the SideStore source)
set -euo pipefail
shopt -s inherit_errexit # failures inside $(...) stop the script too
cd "$(dirname "$0")/.."
ROOT=$PWD
VERSION=$(cat ios/VERSION)
export DEVELOPER_DIR=${DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}
ARCHIVE=ios/build/CanalPeer.xcarchive
STAGE=ios/build/ipa

# privacy_check fails if anything we ship names the person who built it:
# CANALPEER_PRIVATE is a grep -E pattern, such as "myuser|mysurname".
privacy_check() { # dir
	local hits
	hits=$(grep -rlaiE "${CANALPEER_PRIVATE:?set it to the words no build may contain, e.g. your user name}" "$1" || true)
	if [ -n "$hits" ]; then
		echo "privacy check failed, these files carry the builder's identity:" >&2
		echo "$hits" >&2
		exit 1
	fi
}

release() {
	make ios-framework ios-project >/dev/null
	rm -rf "$ARCHIVE" "$STAGE"
	(cd ios && xcodebuild archive -project CanalPeer.xcodeproj -scheme CanalPeer -configuration Release \
		-destination generic/platform=iOS -archivePath "$ROOT/$ARCHIVE" \
		CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY= DEVELOPMENT_TEAM= -quiet)
	mkdir -p "$STAGE/Payload" dist
	cp -R "$ARCHIVE/Products/Applications/CanalPeer.app" "$STAGE/Payload/"
	# VLCKit is LGPL-2.1: its notice ships with the app (Outfit's OFL is in Fonts/).
	cp ios/Vendor/COPYING.txt "$STAGE/Payload/CanalPeer.app/VLCKit-COPYING.txt"
	privacy_check "$STAGE/Payload"
	local ipa=dist/CanalPeer-$VERSION-ios.ipa
	rm -f "$ipa" && (cd "$STAGE" && zip -qr -X "$ROOT/$ipa" Payload)
	echo "$ipa"
}

publish() {
	local f=CanalPeer-$VERSION-ios.ipa
	[ -f "dist/$f" ] || { echo "dist/$f missing: run release first" >&2; exit 1; }
	set -a; source "${CANALPEER_R2_ENV:?the env file with the bucket credentials}"; set +a
	local s3=(--endpoint-url "${CANALPEER_R2_ENDPOINT:?the bucket S3 endpoint}" --region auto)
	# Downloads are cached as immutable for a year: a published name is never reused.
	if AWS_ACCESS_KEY_ID=$ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$SECRET_ACCESS_KEY \
		aws s3api head-object --bucket canalpeer --key "$f" "${s3[@]}" >/dev/null 2>&1; then
		echo "$f is already published: bump ios/VERSION" >&2
		exit 1
	fi
	AWS_ACCESS_KEY_ID=$ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$SECRET_ACCESS_KEY \
	aws s3 cp "dist/$f" "s3://canalpeer/$f" "${s3[@]}" \
		--content-type application/octet-stream --content-disposition "attachment; filename=$f" \
		--cache-control "public, max-age=31536000, immutable" --only-show-errors
	echo "https://dl.canalpeer.com/$f"
	echo "size $(stat -f %z "dist/$f") sha256 $(shasum -a 256 "dist/$f" | cut -d' ' -f1)"
}

case "${1:-}" in
	release) release ;;
	publish) publish ;;
	*) echo "usage: $0 release|publish" >&2; exit 2 ;;
esac
