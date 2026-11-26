#!/usr/bin/env bash
# Builds a signed Android release from the current tree as dist/CanalPeer-<VERSION>-android.apk
# and uploads it to the R2 bucket "canalpeer", served at https://dl.canalpeer.com/.
# Installed copies learn about it once canalpeer-web's web/public/app/latest.json
# points at it.
#
# Bump VERSION (shared by every app) first (major.minor.patch): the name is the version, and downloads
# are cached as immutable, so a name is never reused.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a
# The maintainer's own: the folder with release.env (the APK signing key's
# passwords), the env file with the bucket credentials, and its S3 endpoint.
: "${CANALPEER_SECRETS:?} ${CANALPEER_R2_ENV:?} ${CANALPEER_R2_ENDPOINT:?}"
source "$CANALPEER_SECRETS/release.env"
source "$CANALPEER_R2_ENV"
set +a
version=$(cat VERSION)
# The same pattern as the desktop zips: CanalPeer-<version>-<platform>.<ext>.
apk="CanalPeer-$version-android.apk"
make android-release
mkdir -p dist
cp android/app/build/outputs/apk/release/app-release.apk "dist/$apk"
AWS_ACCESS_KEY_ID=$ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$SECRET_ACCESS_KEY \
  aws s3 cp "dist/$apk" "s3://canalpeer/$apk" \
  --endpoint-url "$CANALPEER_R2_ENDPOINT" --region auto \
  --content-type application/vnd.android.package-archive \
  --content-disposition "attachment; filename=$apk" \
  --cache-control "public, max-age=31536000, immutable" --only-show-errors
echo "https://dl.canalpeer.com/$apk"
scripts/publish-github.sh android "$version" "dist/$apk"
