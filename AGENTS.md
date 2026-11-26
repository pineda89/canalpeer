# AGENTS.md

Guidance for coding agents working in this repository. Read `README.md` too.

CanalPeer native players: each device joins the AceStream swarm itself and plays
the live stream, with no server in between. The server-side web player is the
sibling repo `../canalpeer-web` (canalpeer.com), which also announces releases.

## Commands

The `Makefile` sets `JAVA_HOME` (openjdk@21), `ANDROID_HOME` and `ANDROID_NDK_HOME`;
run Android builds through `make`, or export those first.

```sh
make test                         # Go core (core/)
make desktop-test                 # desktop app (desktop/)
make android-test                 # Android JVM unit tests
make android-apk                  # rebuilds core AAR via gomobile, then debug APK
make android-aar                  # only the gomobile bind -> android/app/libs/mobile.aar

cd core && go test -run TestName ./mobile/
cd android && ./gradlew testDebugUnitTest --tests 'com.canalpeer.tv.DecodingTest'
cd android && ./gradlew lintDebug

ACEWEB_LIVE_CID=<id> go test -tags live -run TestLiveChannel -v ./ace/   # in core/, real swarm
```

Kotlin changes that touch the core API need `make android-aar` first: the AAR is
built from `core/mobile`, not fetched.

## Architecture

- **One version for everything**: `VERSION` at the root drives Android
  (`versionName`/`versionCode`), Windows and Mac (`-X main.version`). iOS is
  parked and has its own `ios/VERSION`; nothing iOS is published.
- **`core/`** (module `canalpeer.com/core`) is the shared Go:
  - `ace/`: the AceStream P2P protocol (tracker, signed handshake, peers,
    live reassembly). canalpeer-web carries an older copy in `internal/ace`;
    a fix that applies to both is ported by hand.
  - `mobile/`: the API every app binds to. `Start(Options)` returns a `Session`
    that serves the channel as a local MPEG-TS HTTP stream; `Sync(cacheDir)`
    refreshes the channel descriptor archive. Descriptors are resolved from a
    device cache, then canalpeer.com, then AceStream hosts (blocked during
    LaLiga matches). `descriptors.json.gz` is a snapshot baked into the apps
    (`make snapshot`). Movies (`vod*.go`) are plain torrents via anacrolix.
    `pcr.go` measures real bitrate because descriptor bitrates lie.
    `roots.go` embeds ISRG Root X1 (missing on Android 7.0).
  - `changelog/changelog.json`: release notes (en + es) for every version,
    embedded. Read by Android (`mobile.Notes`), desktop (directly), the release
    scripts (`go run ./cmd/changelog notes|ready|markdown <v>`) and copied to
    canalpeer-web. Items starting with "Android"/"Windows"/"Mac" are filtered
    per platform. `make release` refuses to run unless the current `VERSION`
    has a dated entry.
- **`android/`** (`com.canalpeer.tv`, minSdk 24, Media3/ExoPlayer): Fire TV and
  Android TV first, phones too. `PlayerActivity` plays the core's local stream.
  TV mode = no touchscreen OR leanback/television/fire_tv feature (TV emulators
  claim a touchscreen). Pure logic lives in small files (`Controls.kt`,
  `Decoding.kt`, `Notes.kt`, ...) so it is unit-testable without a device.
  Strings exist in `values/` plus es, fr, de, it, pt; `StringsTest` enforces
  identical keys and placeholders.
- **`desktop/`**: one Go program driving a bundled official mpv over JSON IPC.
  Opened by hand it shows `launcher.html` (WebView2 / WKWebView); opened from an
  `acestream://` link it goes straight to mpv. On-video controls are
  `controls.lua`. Self-updates silently on close. Platform code is split by
  build-tag suffixes (`_windows`, `_darwin`, `_unix`, `_other`).
- **Updates**: apps query canalpeer.com and this repository's latest GitHub
  release at once and take the highest version, so the app keeps working if
  canalpeer.com is blocked.

## Rules

- **Never publish or deploy without an explicit instruction** ("publica",
  "saca la x.y.z"). Testing requests are not permission. A published version
  name is spent forever (R2 refuses overwrites).
- **Releases** are cut by the maintainer with `make release`: tests, the
  channel snapshot, Android, then the desktop apps, each uploaded and announced
  as it is built. It requires a clean tree and builds from HEAD. The steps
  around it (the site's announcement and its deployment) are the maintainer's
  and are not in this repository.
- **Privacy is non-negotiable**: nothing from the build machine (owner's name,
  email, home paths, other domains) may reach an APK or binary. That is why the
  core is bound from `/tmp/canalpeer-build` with `-trimpath`. The publish
  scripts grep for identity strings; do not weaken them.
- Signing keys and R2 credentials live outside the repo; never commit them.
- Other sessions work in this repo at the same time: stage explicit paths,
  never `git add -A`. Work on a branch and open a PR.
- Code, comments and commits in English.
