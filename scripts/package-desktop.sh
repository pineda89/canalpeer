#!/usr/bin/env bash
# Packages the CanalPeer desktop app with a bundled official mpv.
#   package-desktop.sh mac      -> desktop/build.noindex/CanalPeer.app (.noindex: Spotlight skips it)
#   package-desktop.sh linux x64|arm64 -> dist/CanalPeer-<v>-linux-<arch>.tar.gz (plays with the system's mpv)
#   package-desktop.sh release  -> dist/CanalPeer-<v>-windows-x64.exe (the download), -windows-x64.zip
#                                  (for 1.0.x self-updaters), -mac.zip and both -linux tarballs, privacy-checked
#   package-desktop.sh publish  -> release, then upload them to the canalpeer bucket and GitHub
#   package-desktop.sh publish-linux -> only the Linux tarballs, into the release VERSION already has
# Builds from a neutral directory with -trimpath: nothing from this machine
# (paths carry the owner's name) may end up in what we ship.
set -euo pipefail
shopt -s inherit_errexit # failures inside $(...) stop the script too
cd "$(dirname "$0")/.."
ROOT=$PWD
VERSION=$(cat VERSION) # shared by every app
MPV_VERSION=v0.41.0
MPV_MAC_SHA=5c96f9b21355fc0a11d2e2161ad65f33031070e9fb3f6bd9865fb459b94587e6
MPV_WIN_SHA=4e197f729f5071c6772f35fffd96e0f36e3e8a044bd9479b136bb09b7c6a80ff
CACHE=${CANALPEER_CACHE:-$HOME/.cache/canalpeer}
BUILDDIR=/tmp/canalpeer-build
OUT=desktop/build.noindex

fetch() { # name sha
	mkdir -p "$CACHE"
	[ -f "$CACHE/$1" ] || curl -fsSL -o "$CACHE/$1" "https://github.com/mpv-player/mpv/releases/download/$MPV_VERSION/$1"
	if ! echo "$2  $CACHE/$1" | shasum -a 256 -c - >/dev/null 2>&1; then
		rm -f "$CACHE/$1" # a cut-off download must not stay cached
		echo "checksum mismatch for $1" >&2
		exit 1
	fi
}

# The bundled mpv is GPL-2.0-or-later: ship the notice with every package.
license_notice() { # dir
	cat > "$1/THIRD-PARTY-NOTICES.txt" <<NOTICE
CanalPeer includes mpv $MPV_VERSION (https://mpv.io), unmodified official build,
licensed under the GNU General Public License version 2 or later.
Source code: https://github.com/mpv-player/mpv/tree/$MPV_VERSION
License text: https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt
NOTICE
}

stage() { # copy sources to a neutral path
	scripts/snapshot-descriptors.py # the channels the site knows, built in
	rm -rf "$BUILDDIR" && mkdir -p "$BUILDDIR"
	rsync -a core desktop --exclude build --exclude build.noindex "$BUILDDIR/"
}

icon_icns() { # out.icns
	local set=$BUILDDIR/canalpeer.iconset; mkdir -p "$set"
	local svg=desktop/linux/canalpeer.svg
	for s in 16 32 128 256 512; do
		rsvg-convert -w $s -h $s "$svg" -o "$set/icon_${s}x${s}.png"
		rsvg-convert -w $((s*2)) -h $((s*2)) "$svg" -o "$set/icon_${s}x${s}@2x.png"
	done
	iconutil -c icns "$set" -o "$1"
}

mac() {
	fetch mpv-$MPV_VERSION-macos-14-arm.zip $MPV_MAC_SHA
	stage
	(cd "$BUILDDIR/desktop" && GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
		go build -trimpath -tags disable_libutp -ldflags="-s -w -X main.version=$VERSION" -o "$BUILDDIR/canalpeer" .)
	local app=$OUT/CanalPeer.app
	rm -rf "$app" && mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
	cp "$BUILDDIR/canalpeer" "$app/Contents/MacOS/canalpeer"
	sed "s/VERSION/$VERSION/g" desktop/mac/Info.plist > "$app/Contents/Info.plist"
	icon_icns "$app/Contents/Resources/canalpeer.icns"
	# The player window belongs to the bundled mpv.app: give it our name and icon
	# so the Dock shows CanalPeer, then re-sign it ad hoc (free, no Apple account).
	# The Dock names a running app after its bundle folder, not CFBundleName: the
	# folder is CanalPeer.app too (it was "CanalPeer Player.app" up to 1.5.1).
	local tmp=$BUILDDIR/mpvzip; mkdir -p "$tmp"
	unzip -q -o "$CACHE/mpv-$MPV_VERSION-macos-14-arm.zip" -d "$tmp"
	tar xzf "$tmp/mpv.tar.gz" -C "$tmp"
	local player="$app/Contents/Resources/CanalPeer.app"
	mv "$tmp/mpv.app" "$player"
	plutil -replace CFBundleName -string "CanalPeer" "$player/Contents/Info.plist"
	plutil -replace CFBundleDisplayName -string "CanalPeer" "$player/Contents/Info.plist" 2>/dev/null || \
		plutil -insert CFBundleDisplayName -string "CanalPeer" "$player/Contents/Info.plist"
	plutil -replace CFBundleIdentifier -string "com.canalpeer.player" "$player/Contents/Info.plist"
	plutil -remove CFBundleDocumentTypes "$player/Contents/Info.plist" 2>/dev/null || true
	plutil -remove CFBundleURLTypes "$player/Contents/Info.plist" 2>/dev/null || true
	local icon; icon=$(plutil -extract CFBundleIconFile raw "$player/Contents/Info.plist")
	cp "$app/Contents/Resources/canalpeer.icns" "$player/Contents/Resources/${icon%.icns}.icns"
	# macOS prefers the asset catalog (CFBundleIconName -> Assets.car, mpv's own
	# icon) over CFBundleIconFile: drop both so the Dock shows our icns.
	plutil -remove CFBundleIconName "$player/Contents/Info.plist" 2>/dev/null || true
	rm -f "$player/Contents/Resources/Assets.car"
	codesign --force --deep -s - "$player" 2>/dev/null
	codesign --force -s - "$app" 2>/dev/null
	echo "$app"
}

windows() {
	fetch mpv-$MPV_VERSION-x86_64-pc-windows-msvc.zip $MPV_WIN_SHA
	stage
	# The exe carries mpv and writes it to %LOCALAPPDATA% on first run: one file to download.
	local bin=$BUILDDIR/desktop/mpvbin raw=$BUILDDIR/mpvraw
	mkdir -p "$raw" && unzip -q -o "$CACHE/mpv-$MPV_VERSION-x86_64-pc-windows-msvc.zip" mpv.exe vulkan-1.dll -d "$raw"
	license_notice "$raw"
	echo "$MPV_VERSION" > "$bin/VERSION"
	(cd "$raw" && shasum -a 256 mpv.exe vulkan-1.dll THIRD-PARTY-NOTICES.txt) > "$bin/SHA256SUMS"
	for f in mpv.exe vulkan-1.dll THIRD-PARTY-NOTICES.txt; do gzip -9 -n -c "$raw/$f" > "$bin/$f.gz"; done
	local png=$BUILDDIR/icon.png
	rsvg-convert -w 256 -h 256 desktop/linux/canalpeer.svg -o "$png"
	(cd "$BUILDDIR/desktop" && go run github.com/tc-hib/go-winres@v0.3.3 simply \
		--icon "$png" --manifest gui --product-name CanalPeer --file-version "$VERSION" --product-version "$VERSION" >/dev/null && \
		GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -H windowsgui -X main.version=$VERSION" -o "$BUILDDIR/win/CanalPeer.exe" .)
	echo "$BUILDDIR/win"
}

# Linux: one static binary that loads GTK/WebKit for its launcher at run time
# (pure Go, so both architectures cross-compile here) and plays with the
# distribution's mpv, which is why the tarball carries no GPL player.
linux() { # x64|arm64
	local arch=$1 goarch
	case $arch in x64) goarch=amd64 ;; arm64) goarch=arm64 ;; *) echo "linux x64|arm64" >&2; exit 2 ;; esac
	stage
	local top=$BUILDDIR/linux-$arch dir=$BUILDDIR/linux-$arch/CanalPeer
	mkdir -p "$dir"
	(cd "$BUILDDIR/desktop" && GOOS=linux GOARCH=$goarch CGO_ENABLED=0 \
		go build -trimpath -tags disable_libutp -ldflags="-s -w -X main.version=$VERSION" -o "$dir/canalpeer" .)
	cat > "$dir/THIRD-PARTY-NOTICES.txt" <<NOTICE
CanalPeer plays video with mpv (https://mpv.io) installed from your distribution;
it does not include mpv. mpv is licensed under the GNU General Public License
version 2 or later: https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt
NOTICE
	privacy_check "$dir"
	mkdir -p dist
	local out=dist/CanalPeer-$VERSION-linux-$arch.tar.gz
	# Owner root and no macOS metadata: nothing of this machine in the archive.
	COPYFILE_DISABLE=1 tar -C "$top" --uid 0 --gid 0 --uname root --gname root --no-mac-metadata --no-xattrs -czf "$ROOT/$out" CanalPeer
	echo "$out"
}

# privacy_check fails if anything we ship names the person who built it:
# CANALPEER_PRIVATE is a grep -E pattern, such as "myuser|mysurname".
privacy_check() { # dir
	local hits
	hits=$(grep -rlaiE "${CANALPEER_PRIVATE:?set it to the words no build may contain, e.g. your user name}" "$1" || true) # not "/Users/": mpv's own CI paths (/Users/runner, /Users/brew) are fine
	if [ -n "$hits" ]; then
		echo "privacy check failed, these files carry the builder's identity:" >&2
		echo "$hits" >&2
		exit 1
	fi
}

release() {
	# A release without the key could never update itself again.
	if grep -q '^var updateKey = ""' desktop/update.go; then
		echo "desktop/update.go has no updateKey: cd core && go run ./cmd/sign gen <env-file>, then paste the public key" >&2
		exit 1
	fi
	mkdir -p dist
	local exe=dist/CanalPeer-$VERSION-windows-x64.exe win=dist/CanalPeer-$VERSION-windows-x64.zip mac_zip=dist/CanalPeer-$VERSION-mac.zip
	windows >/dev/null
	local w=$BUILDDIR/win
	license_notice "$w"
	privacy_check "$w"
	cp "$w/CanalPeer.exe" "$exe"
	# What installed 1.0.x self-updaters download: a zip with CanalPeer.exe in it.
	rm -f "$win" && (cd "$w" && zip -qr "$ROOT/$win" CanalPeer.exe THIRD-PARTY-NOTICES.txt)
	mac >/dev/null
	license_notice "$OUT/CanalPeer.app/Contents/Resources"
	codesign --force -s - "$OUT/CanalPeer.app" 2>/dev/null
	privacy_check "$OUT/CanalPeer.app"
	rm -f "$mac_zip" && ditto -c -k --norsrc --noextattr --noacl --keepParent "$OUT/CanalPeer.app" "$mac_zip"
	linux x64; linux arm64
	echo "$exe"; echo "$win"; echo "$mac_zip"
}

# ship builds the given names into dist/ with build, then puts them on R2 and
# in the GitHub release of VERSION. A name already on R2 stops it before building.
ship() { # build-function names...
	local build=$1; shift
	set -a; source "${CANALPEER_R2_ENV:?the env file with the bucket credentials}"; set +a
	set -a; source "${CANALPEER_SECRETS:?the folder with update-key.env}/update-key.env"; set +a # signs the archives (publish-github.sh)
	: "${CANALPEER_R2_ENDPOINT:?the bucket S3 endpoint}"
	# Downloads are cached as immutable for a year: a published name is never reused.
	for f in "$@"; do
		if AWS_ACCESS_KEY_ID=$ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$SECRET_ACCESS_KEY \
			aws s3api head-object --bucket canalpeer --key "$f" \
			--endpoint-url "$CANALPEER_R2_ENDPOINT" --region auto >/dev/null 2>&1; then
			echo "$f is already published: bump VERSION" >&2
			exit 1
		fi
	done
	$build
	for f in "${@/#/dist/}"; do
		local type=application/zip
		[[ $f == *.exe ]] && type=application/vnd.microsoft.portable-executable
		[[ $f == *.tar.gz ]] && type=application/gzip
		AWS_ACCESS_KEY_ID=$ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$SECRET_ACCESS_KEY \
		aws s3 cp "$f" "s3://canalpeer/$(basename "$f")" \
			--endpoint-url "$CANALPEER_R2_ENDPOINT" --region auto \
			--content-type "$type" --content-disposition "attachment; filename=$(basename "$f")" \
			--cache-control "public, max-age=31536000, immutable" --only-show-errors
		echo "https://dl.canalpeer.com/$(basename "$f")"
	done
	scripts/publish-github.sh desktop "$VERSION" "${@/#/dist/}"
}

publish() {
	ship release CanalPeer-$VERSION-windows-x64.exe CanalPeer-$VERSION-windows-x64.zip CanalPeer-$VERSION-mac.zip \
		CanalPeer-$VERSION-linux-x64.tar.gz CanalPeer-$VERSION-linux-arm64.tar.gz
}

# publish-linux adds the Linux builds to a version already out for the other
# systems. desktop.json keeps its version, so no other system is offered anything.
linux_both() { linux x64; linux arm64; }
publish_linux() {
	ship linux_both CanalPeer-$VERSION-linux-x64.tar.gz CanalPeer-$VERSION-linux-arm64.tar.gz
}

case "${1:-}" in
	mac) mac ;;
	windows) windows ;;
	linux) linux "${2:-}" ;;
	release) release ;;
	publish) publish ;;
	publish-linux) publish_linux ;;
	*) echo "usage: $0 mac|windows|linux x64|arm64|release|publish|publish-linux" >&2; exit 2 ;;
esac
