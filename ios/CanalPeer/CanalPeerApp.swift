import AVFoundation
import Mobile
import SwiftUI

@main
struct CanalPeerApp: App {
    @UIApplicationDelegateAdaptor private var appDelegate: AppDelegate
    @State private var playing: String?

    init() {
        // Plays with the silent switch on, like any video app.
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .moviePlayback)
        MobileSync(cacheDir)
    }

    var body: some Scene {
        WindowGroup {
            HomeView(play: { playing = $0 })
                .onOpenURL { url in
                    if let id = normalize(url.absoluteString) { playing = id }
                }
                .fullScreenCover(item: Binding(
                    get: { playing.map(Channel.init) },
                    set: { playing = $0?.id }
                )) { ch in
                    PlayerScreen(id: ch.id)
                }
        }
    }
}

/// Owns the allowed orientations, so the player's full-screen button can pin landscape.
final class AppDelegate: NSObject, UIApplicationDelegate {
    static var orientations: UIInterfaceOrientationMask = .allButUpsideDown

    func application(_ application: UIApplication, supportedInterfaceOrientationsFor window: UIWindow?) -> UIInterfaceOrientationMask {
        Self.orientations
    }
}

struct Channel: Identifiable { let id: String }

let cacheDir = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].path

/// The core's own parsing: a content id or any acestream:// link, nil if neither.
func normalize(_ s: String) -> String? {
    var err: NSError?
    let id = MobileNormalize(s, &err)
    return err == nil && !id.isEmpty ? id : nil
}

let appVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
