export JAVA_HOME ?= $(shell brew --prefix openjdk@21)
export ANDROID_HOME ?= /opt/homebrew/share/android-commandlinetools
export ANDROID_NDK_HOME ?= $(ANDROID_HOME)/ndk/29.0.14206865
export PATH := $(JAVA_HOME)/bin:$(HOME)/go/bin:$(PATH)
# Nothing from this machine (paths carry the owner's name) may end up in an app:
# -trimpath covers Go sources, and binding from a copy at a neutral path keeps
# the project directory out of the build info gomobile's wrapper module records.
BUILDDIR := /tmp/canalpeer-build

.PHONY: release test snapshot android-aar android-test android-apk android-release desktop-test desktop-mac ios-vendor ios-framework ios-project ios-device

test:
	cd core && go test ./...

# The channels the site knows, built into the apps.
snapshot:
	scripts/snapshot-descriptors.py

# anacrolix/torrent: disable_libutp keeps its C++ uTP out (no libc++ in the APK);
# -checklinkname=0 lets pion link to net internals on Android.
android-aar: snapshot
	rm -rf $(BUILDDIR) && mkdir -p $(BUILDDIR)
	rsync -a core/ $(BUILDDIR)/core/
	cd $(BUILDDIR)/core && $(HOME)/go/bin/gomobile bind -trimpath -tags disable_libutp -ldflags="-s -w -checklinkname=0" -target=android/arm,android/arm64,android/amd64 -androidapi 24 -o $(CURDIR)/android/app/libs/mobile.aar ./mobile

android-test:
	cd android && ./gradlew testDebugUnitTest

android-apk: android-aar
	cd android && ./gradlew assembleDebug

# Publishes VERSION on every platform, in order.
release:
	@test -z "$$(git status --porcelain)" || { echo "commit or stash first: releases build from HEAD" >&2; exit 1; }
	cd core && go run ./cmd/changelog ready "$$(cat ../VERSION)" # notes and date in core/changelog/changelog.json
	$(MAKE) test desktop-test android-test
	scripts/snapshot-descriptors.py
	scripts/publish-android.sh
	scripts/package-desktop.sh publish

android-release: android-aar
	cd android && ./gradlew assembleRelease

desktop-test:
	cd desktop && go test ./...

desktop-mac:
	scripts/package-desktop.sh mac

# VLCKit plays the core's MPEG-TS (AVPlayer only takes HLS); a pinned binary
# from VideoLAN, kept out of git.
VLCKIT := MobileVLCKit-3.3.17-21ce5c0e-cb1e02fb
VLCKIT_SHA256 := fb2d38633fec6eb2ecbad56dc99ed765f3b416708fe48c50ca6a79f7a7012ac1

ios-vendor:
	@test -d ios/Vendor/MobileVLCKit.xcframework || { \
	  mkdir -p ios/Vendor && cd ios/Vendor && \
	  curl -sSfLO https://download.videolan.org/pub/cocoapods/prod/$(VLCKIT).tar.xz && \
	  echo "$(VLCKIT_SHA256)  $(VLCKIT).tar.xz" | shasum -a 256 -c && \
	  tar -xJf $(VLCKIT).tar.xz --strip-components 1 MobileVLCKit-binary/MobileVLCKit.xcframework MobileVLCKit-binary/COPYING.txt && \
	  rm $(VLCKIT).tar.xz; }

ios-framework: snapshot
	rm -rf $(BUILDDIR) && mkdir -p $(BUILDDIR)
	rsync -a core/ $(BUILDDIR)/core/
	cd $(BUILDDIR)/core && $(HOME)/go/bin/gomobile bind -trimpath -tags disable_libutp -ldflags="-s -w -checklinkname=0" -target=ios,iossimulator -iosversion 16.0 -o $(CURDIR)/ios/Frameworks/Mobile.xcframework ./mobile

ios-project: ios-vendor
	cd ios && CANALPEER_VERSION=$$(cat VERSION) xcodegen generate --quiet

# Debug build on the iPhone connected by cable. TEAM is the Apple ID's personal
# team (Xcode > Settings > Accounts) and DEVICE its UDID (`xcrun devicectl list
# devices`); neither goes in git. The first install needs Developer Mode on and
# the certificate trusted in Settings > General > VPN & Device Management.
ios-device: ios-framework ios-project
	cd ios && xcodebuild -project CanalPeer.xcodeproj -scheme CanalPeer -configuration Debug \
	  -destination id=$(DEVICE) -derivedDataPath build -allowProvisioningUpdates \
	  -allowProvisioningDeviceRegistration DEVELOPMENT_TEAM=$(TEAM) -quiet build
	xcrun devicectl device install app --device $(DEVICE) $(CURDIR)/ios/build/Build/Products/Debug-iphoneos/CanalPeer.app
