#!/usr/bin/env bash
# Publishes a release on this project's GitHub repo, so updates and downloads
# survive a block of canalpeer.com. Once the files are up, adds the version
# file the apps ask the latest release for:
#
#   publish-github.sh desktop <version> <files...>   -> desktop.json
#   publish-github.sh android <version> <apk>        -> latest.json
#
# Needs `gh auth login` done once for each of the two accounts below; the
# active gh account is left alone.
set -euo pipefail
platform=$1 version=$2; shift 2
repo=pineda89/canalpeer tag=v$version # one release per version, every platform's files in it
GH_TOKEN=$(gh auth token --user "${repo%%/*}")
export GH_TOKEN
dl=https://github.com/$repo/releases/download/$tag

# A private repo's releases answer 404 to the apps, and the version files
# would send every install there.
if [ "$(gh repo view "$repo" --json visibility -q .visibility)" != PUBLIC ]; then
	echo "$repo is not public: nothing can download a release from it" >&2
	exit 1
fi

# Installs up to 1.6 ask canalpeer/canalpeer's main for the version file, so
# it follows, pointing at the files here.
# shortcut: kept for those installs; delete `old` and `put` once they are gone.
old=canalpeer/canalpeer
put() {
	local sha
	sha=$(GH_TOKEN=$(gh auth token --user canalpeer) gh api "repos/$old/contents/$1" -q .sha 2>/dev/null || true)
	GH_TOKEN=$(gh auth token --user canalpeer) gh api -X PUT "repos/$old/contents/$1" -f message="$2" \
		-f content="$(printf '%s' "$3" | base64)" ${sha:+-f sha="$sha"} \
		-f "committer[name]=canalpeer" -f "committer[email]=canalpeer@users.noreply.github.com" >/dev/null
}

if assets=$(gh release view "$tag" -R "$repo" --json assets -q '.assets[].name' 2>/dev/null); then
	for f in "$@"; do
		if grep -qx "$(basename "$f")" <<<"$assets"; then
			echo "$(basename "$f") is already in $tag: bump VERSION" >&2
			exit 1
		fi
	done
	gh release upload "$tag" -R "$repo" "$@"
else
	notes=$(cd core && go run ./cmd/changelog notes "$version")
	gh release create "$tag" -R "$repo" --target master --title "CanalPeer $version" --notes "$notes" "$@"
	# The whole history, in English, where it has always been.
	put CHANGELOG.md "changelog $version" "$(cd core && go run ./cmd/changelog markdown)"
fi

case $platform in
desktop)
	file=desktop.json
	# Installed apps only take an archive signed with the key whose public half
	# they carry (updateKey). The same "sig" goes into canalpeer-web's desktop.json.
	: "${CANALPEER_UPDATE_KEY:?the update signing key (made by core/cmd/sign gen)}"
	export CANALPEER_UPDATE_KEY
	sig() { (cd core && go run ./cmd/sign "../dist/CanalPeer-$version-$1"); }
	sw=$(sig windows-x64.zip) sm=$(sig mac.zip) sx=$(sig linux-x64.tar.gz) sa=$(sig linux-arm64.tar.gz)
	body=$(printf '{\n  "version": "%s",\n  "windows": "%s",\n  "windowsExe": "%s",\n  "mac": "%s",\n  "linuxX64": "%s",\n  "linuxArm64": "%s",\n  "sig": {"windows": "%s", "mac": "%s", "linuxX64": "%s", "linuxArm64": "%s"}\n}\n' "$version" \
		"$dl/CanalPeer-$version-windows-x64.zip" "$dl/CanalPeer-$version-windows-x64.exe" "$dl/CanalPeer-$version-mac.zip" \
		"$dl/CanalPeer-$version-linux-x64.tar.gz" "$dl/CanalPeer-$version-linux-arm64.tar.gz" "$sw" "$sm" "$sx" "$sa")
	echo "$body" ;;
android)
	file=latest.json
	body=$(printf '{\n  "version": "%s",\n  "url": "%s"\n}\n' "$version" "$dl/CanalPeer-$version-android.apk") ;;
*) echo "unknown platform $platform" >&2; exit 2 ;;
esac
tmp=$(mktemp -d)
printf '%s' "$body" >"$tmp/$file"
gh release upload "$tag" -R "$repo" --clobber "$tmp/$file"
rm -r "$tmp"
put "$file" "$platform $version" "$body"
echo "https://github.com/$repo/releases/latest/download/$file -> $version"
