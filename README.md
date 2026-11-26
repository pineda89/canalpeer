# CanalPeer apps

Native CanalPeer players: each device joins the AceStream swarm itself and plays
the live stream, with no server in between. The web player with a server is a
separate project (canalpeer-web, canalpeer.com).

## Layout

| Path | What |
|---|---|
| `core/` | Go shared by every app. `ace/` speaks the P2P protocol; `mobile/` turns a channel into a local MPEG-TS stream and is what the apps bind to. |
| `android/` | Android TV app (Fire TV first).  |
| `docs/` | Designs and plans. |
| `scripts/` | Release scripts. |
| `ios/` | iPhone/iPad app (SwiftUI + VLCKit). **Parked: not distributed** (see below). Version in `ios/VERSION`. |

Desktop apps live in `desktop/`. Every app shares the repo's `VERSION`: Android, Windows, Mac and Linux always carry
the same version, published together as one GitHub release.

## What the apps send

There is no account and no switch for any of this.

- **To canalpeer.com**
  - When the app opens: a random id made on first run and kept on the device,
    the platform and the version (`core/stats`).
  - When a channel starts playing: its content id, without the install id.
  - To play a channel it does not know: the content id it asks for. A channel
    the device resolved on its own is asked for there too, so the site's
    archive learns it (`core/mobile/resolve.go`).
  - The update check, also asked of this repository's latest release on
    GitHub. Its `mirror` branch serves a copy of the channel archive, which
    `.github/workflows/mirror.yml` refreshes every hour.
- **To the swarm.** Trackers and peers see the device's IP address, and the
  device uploads the pieces it holds to other peers. Each piece is checked
  against the channel's signature when the descriptor carries the channel's
  key; one from the site or a mirror may not, and then plays unchecked.
- **To Cloudflare, with the VPN.** "On", or "Automatic" once peers do not
  answer, registers a WARP device with a fresh key and no identifying field
  (`core/warp`), and sends the traffic above through it.
- **On the local network.** The TV app takes "play" and "stop" from any device
  on the LAN without asking, and a channel sent to a DLNA TV is served to the
  LAN while it plays.

## Building from a clone

`make test` and `make desktop-test` need only Go. A build made without
`make snapshot` carries no channels of its own: the repo's
`core/mobile/descriptors.json.gz` is empty on purpose, and the snapshot the
release builds in is ignored by git.

The release scripts are the maintainer's: they read keys from outside the repo
and publish to the project's own storage.

## Desktop (Windows, Mac, Linux)

One Go program drives mpv over its JSON IPC: a bundled official build on
Windows and Mac, the distribution's own on Linux. Opened by hand it
shows a launcher (WebView2 on Windows, WKWebView on Mac, WebKitGTK 4.1 on
Linux, loaded at run time with purego so a missing WebKit only loses the
launcher; `launcher.html`);
opened from an `acestream://` link it goes straight to mpv. Stop, Esc or the
player's close button bring the launcher back; closing the launcher quits. The
controls over the video are `controls.lua`.

`scripts/package-desktop.sh release` writes:

- `CanalPeer-<v>-windows-x64.exe`: the download. It carries mpv (gzipped in
  `desktop/mpvbin/`, filled by the script) and writes it to
  `%LOCALAPPDATA%\CanalPeer\mpv\<mpv version>\` on first run.
- `CanalPeer-<v>-windows-x64.zip`: only for self-updates; installed 1.0.x
  expect a zip with `CanalPeer.exe` in it. `desktop.json`'s `windows` points
  here, `windowsExe` at the exe.
- `CanalPeer-<v>-mac.zip`: the `.app`.
- `CanalPeer-<v>-linux-{x64,arm64}.tar.gz`: `CanalPeer/canalpeer`, a pure-Go
  binary (cross-compiled here, `CGO_ENABLED=0`). Its first run adds
  `~/.local/share/applications/canalpeer.desktop` and claims `acestream://`
  with `xdg-mime`; updates replace the binary in place (`linuxX64` and
  `linuxArm64` in `desktop.json`).

`publish` uploads the five.

## iOS

**Parked.** The app works, but it is not distributed: SideStore proved too
fragile to ask of viewers, and anything sturdier needs the paid Apple Developer
Program. Nothing iOS is published and the site has no iPhone download.

Needs Xcode, gomobile and XcodeGen. VLCKit plays the core's MPEG-TS (AVPlayer
only takes HLS); `make ios-vendor` fetches a pinned binary.

```sh
make ios-device TEAM=<personal team> DEVICE=<udid>   # debug build on a cabled iPhone
```

### Release

Bump `ios/VERSION`, then `scripts/package-ios.sh release` writes the unsigned,
privacy-checked `dist/CanalPeer-<version>-ios.ipa` and `publish` uploads it. Viewers install it with
SideStore, which signs it with their own Apple ID (renewed every 7 days).
Publishing it means adding the version on top of `versions` in canalpeer-web's
`web/public/app/ios-source.json`: that file is the SideStore source viewers add,
and the app reads it to tell those who installed by hand that an update exists.

## Android

Needs Go, gomobile, the Android SDK/NDK and JDK 21 (see the `Makefile`).

```sh
make test             # Go core
make android-test     # app unit tests
make android-apk      # debug build: android/app/build/outputs/apk/debug/
```

### Release

Every platform ships together with one version: `make release`, which tests,
builds, uploads and announces each platform in turn. The site's announcement
and its deployment come after it and are not in this repository.

The scripts take what is the publisher's own from the environment:
`CANALPEER_SECRETS` (the folder with `release.env` and `update-key.env`),
`CANALPEER_R2_ENV` and `CANALPEER_R2_ENDPOINT` (the download bucket's
credentials and S3 endpoint) and `CANALPEER_PRIVATE` (words no build may
contain).

The signing key lives outside the repo; losing it means installed copies can no
longer be updated.

Desktop updates are signed too: the app only installs an archive whose SHA-256
is signed (ed25519) by the key whose public half is `updateKey` in
`desktop/update.go`. `cd core && go run ./cmd/sign gen <env-file>` makes the
pair once; `publish-github.sh` signs with `$CANALPEER_UPDATE_KEY` and prints
the `"sig"` block, which the site's `desktop.json` must carry as well. A build
with an empty `updateKey` never updates itself: it only tells the viewer a new
version exists. Losing that key means the same as losing the APK's.

### Privacy

Nothing from the build machine may reach an APK: the module path is neutral and
the core is bound from a neutral directory with `-trimpath`. Before publishing,
grep the unpacked APK for the builder's name.

## License

CanalPeer is free software under the GNU General Public License, version 3
(`LICENSE`). The desktop packages also carry mpv, under its own GPL, and the
iOS app VLCKit, under the LGPL; the Outfit font is under the SIL Open Font
License (`desktop/fonts/OFL.txt`).
