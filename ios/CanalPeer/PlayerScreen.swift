import MediaPlayer
import Mobile
import MobileVLCKit
import SwiftUI

private let seekStepMs: Int64 = 5_000
private let liveMs: Int64 = 13_000 // the core never jumps nearer than ~12 s to the edge

struct PlayerScreen: View {
    let id: String
    @StateObject private var model = PlayerModel()
    @State private var controls = true
    @State private var info = false
    @State private var landscape = false
    @State private var hideTask: Task<Void, Never>?
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            // A UIKit view takes the touches before SwiftUI sees them: taps go to the screen.
            VideoView(player: model.vlc).ignoresSafeArea().allowsHitTesting(false)
            if let text = model.overlay {
                Text(text).font(.outfit(.body)).multilineTextAlignment(.center)
                    .padding(.horizontal, 18).padding(.vertical, 12)
                    .background(Color.panel.opacity(0.9), in: RoundedRectangle(cornerRadius: 8))
                    .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.line, lineWidth: 1))
                    .foregroundStyle(Color.text)
            }
            if controls { controlsLayer.transition(.opacity) }
        }
        .contentShape(Rectangle())
        .onTapGesture { show(!controls) }
        .statusBarHidden(!controls)
        .persistentSystemOverlays(controls ? .automatic : .hidden)
        .onAppear {
            UIApplication.shared.isIdleTimerDisabled = true
            model.start(id)
            show(true)
        }
        .onDisappear {
            UIApplication.shared.isIdleTimerDisabled = false
            model.stop()
            if landscape { rotate(false) }
        }
        // Leaving the app ends the stream: nothing downloads or uploads in the background.
        .onChange(of: scenePhase) { phase in
            if phase == .background { dismiss() }
        }
    }

    private var controlsLayer: some View {
        ZStack {
            Color.bg.opacity(0.45).ignoresSafeArea().allowsHitTesting(false)
            HStack(spacing: 56) {
                button("gobackward.5", "Back 5 seconds") { model.jump(-seekStepMs) }
                button(model.paused ? "play.fill" : "pause.fill", model.paused ? "Play" : "Pause", size: 44) { model.togglePause() }
                button("goforward.5", "Forward 5 seconds") { model.jump(seekStepMs) }
            }
            VStack {
                HStack {
                    button("xmark", "Stop") { dismiss() }
                    Spacer()
                    if let toast = model.toast { Text(toast).font(.outfit(.callout, .semibold)).foregroundStyle(Color.accent) }
                    Spacer()
                    button(info ? "info.circle.fill" : "info.circle", "Info") { info.toggle() }
                    button(landscape ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right",
                           landscape ? "Exit full screen" : "Full screen") { rotate(!landscape) }
                }
                Spacer()
                if info {
                    Text("Peers \(model.peers) · buffer \(model.bufferedMs / 1000) s · v\(appVersion)")
                        .font(.outfit(.footnote).monospacedDigit()).foregroundStyle(Color.muted)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding()
        }
    }

    private func button(_ symbol: String, _ label: String, size: CGFloat = 28, action: @escaping () -> Void) -> some View {
        Button {
            action()
            show(true) // any touch keeps the controls up a while longer
        } label: {
            Image(systemName: symbol).font(.system(size: size)).frame(width: 56, height: 56)
        }
        .foregroundStyle(Color.ink)
        .accessibilityLabel(label)
    }

    // Landscape whatever the phone's rotation lock says; back to following the phone after.
    private func rotate(_ on: Bool) {
        landscape = on
        AppDelegate.orientations = on ? .landscape : .allButUpsideDown
        guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene else { return }
        scene.keyWindow?.rootViewController?.setNeedsUpdateOfSupportedInterfaceOrientations()
        scene.requestGeometryUpdate(.iOS(interfaceOrientations: on ? .landscapeRight : .portrait))
    }

    private func show(_ visible: Bool) {
        withAnimation(.easeInOut(duration: 0.2)) { controls = visible }
        hideTask?.cancel()
        guard visible else { return }
        hideTask = Task {
            try? await Task.sleep(nanoseconds: 4_000_000_000)
            guard !Task.isCancelled, !model.paused else { return }
            withAnimation(.easeInOut(duration: 0.2)) { controls = false }
        }
    }
}

struct VideoView: UIViewRepresentable {
    let player: VLCMediaPlayer

    func makeUIView(context: Context) -> UIView {
        let v = UIView()
        v.backgroundColor = .black
        v.isUserInteractionEnabled = false
        player.drawable = v
        return v
    }

    func updateUIView(_ uiView: UIView, context: Context) {}
}

final class PlayerModel: NSObject, ObservableObject, VLCMediaPlayerDelegate {
    let vlc = VLCMediaPlayer()
    @Published var overlay: String? = "Finding the channel…"
    @Published var peers: Int64 = 0
    @Published var bufferedMs: Int64 = 0
    @Published var paused = false
    @Published var toast: String?

    private var session: MobileSession?
    private var timer: Timer?
    private var opened = false
    private var openedAt = 0 // bumps on every open, so a stale reopen does nothing
    private var shownMs: Int64 = 0 // playback time since the player last opened the stream
    private var toastTask: Task<Void, Never>?

    func start(_ id: String) {
        let o = MobileOptions()
        o.contentID = id
        o.cacheDir = cacheDir
        var err: NSError?
        session = MobileStart(o, &err)
        guard session != nil, err == nil else {
            overlay = "Could not start: check the network."
            return
        }
        vlc.delegate = self
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in self?.poll() }
        remote(on: true)
    }

    // The player goes first: one that stopped reading must not keep the core's server busy.
    func stop() {
        remote(on: false)
        timer?.invalidate()
        timer = nil
        vlc.delegate = nil
        vlc.stop()
        session?.stop()
        session = nil
    }

    // Pausing keeps downloading into the core's queue, so resuming leaves a bigger buffer.
    func togglePause() {
        guard opened else { return }
        if paused { vlc.play() } else { vlc.pause() }
        paused.toggle()
        nowPlaying()
    }

    // Back replays what the core kept; forward drops buffer toward live.
    func jump(_ deltaMs: Int64) {
        guard opened else { return }
        let behind = reopen(deltaMs)
        paused = false
        say(behind <= liveMs ? "Live" : "\(behind / 1000) s behind live")
    }

    private func poll() {
        guard let s = session, let st = s.status() else { return }
        peers = st.peers
        bufferedMs = st.queuedMs
        if vlc.isPlaying { shownMs = Int64(vlc.time.intValue) }
        switch st.phase {
        case "failed":
            timer?.invalidate()
            overlay = switch st.err {
            case "not_found": "This channel does not exist."
            case "no_peers": "Nobody is sharing this channel right now."
            default: "Could not reach the channel: check the network."
            }
            return
        case "playing" where !opened:
            opened = true
            open(s.url())
            nowPlaying()
        default: break
        }
        overlay = switch st.phase {
        case "resolving": "Finding the channel…"
        case "searching": "Looking for peers… (\(st.peers))"
        case "reconnecting": "Reconnecting… (\(st.peers) peers)"
        default: vlc.isPlaying || paused ? nil : "Loading…"
        }
    }

    private func open(_ url: String) {
        guard let u = URL(string: url) else { return }
        openedAt += 1
        let media = VLCMedia(url: u)
        // The server is the core on this same device: silence means the broadcast
        // is late, not a broken link, so give it a generous buffer.
        media.addOptions(["network-caching": 10_000])
        vlc.media = media
        vlc.play()
    }

    // Every reopen goes through here: the core first moves to what is on screen
    // (plus deltaMs), so the player neither skips nor replays. Returns how far
    // behind the newest data playback resumes.
    @discardableResult
    private func reopen(_ deltaMs: Int64 = 0) -> Int64 {
        guard let s = session else { return 0 }
        if vlc.isPlaying { shownMs = Int64(vlc.time.intValue) }
        vlc.stop() // close the stream first, so the old connection reads nothing more
        let behind = s.jump(deltaMs, shownMs: shownMs)
        shownMs = 0
        open(s.url())
        return behind
    }

    // Never leave the viewer on an error: resume the local stream at what was on screen.
    func mediaPlayerStateChanged(_ aNotification: Notification) {
        guard vlc.state == .error || vlc.state == .ended else { return }
        let gen = openedAt
        DispatchQueue.main.asyncAfter(deadline: .now() + 1) { [weak self] in
            guard let self, self.openedAt == gen, !self.paused else { return }
            self.reopen()
        }
    }

    private func say(_ text: String) {
        toast = text
        toastTask?.cancel()
        toastTask = Task { @MainActor in
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            if !Task.isCancelled { toast = nil }
        }
    }

    // Headphones, Control Center and Bluetooth remotes: play/pause and ±5 s.
    private func remote(on: Bool) {
        let c = MPRemoteCommandCenter.shared()
        let cmds = [c.playCommand, c.pauseCommand, c.togglePlayPauseCommand, c.skipForwardCommand, c.skipBackwardCommand]
        cmds.forEach { $0.removeTarget(nil) }
        guard on else {
            MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
            return
        }
        c.skipForwardCommand.preferredIntervals = [NSNumber(value: seekStepMs / 1000)]
        c.skipBackwardCommand.preferredIntervals = [NSNumber(value: seekStepMs / 1000)]
        c.playCommand.addTarget { [weak self] _ in
            if self?.paused == true { self?.togglePause() }
            return .success
        }
        c.pauseCommand.addTarget { [weak self] _ in
            if self?.paused == false { self?.togglePause() }
            return .success
        }
        c.togglePlayPauseCommand.addTarget { [weak self] _ in self?.togglePause(); return .success }
        c.skipForwardCommand.addTarget { [weak self] _ in self?.jump(seekStepMs); return .success }
        c.skipBackwardCommand.addTarget { [weak self] _ in self?.jump(-seekStepMs); return .success }
    }

    private func nowPlaying() {
        MPNowPlayingInfoCenter.default().nowPlayingInfo = [
            MPMediaItemPropertyTitle: "CanalPeer",
            MPNowPlayingInfoPropertyIsLiveStream: true,
            MPNowPlayingInfoPropertyPlaybackRate: paused ? 0.0 : 1.0,
        ]
    }
}
