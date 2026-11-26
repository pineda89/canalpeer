import SwiftUI

/// Launcher screen: paste an id when there is no link to tap.
struct HomeView: View {
    let play: (String) -> Void
    @State private var input = ""
    @State private var bad = false
    @State private var update: String?
    @FocusState private var focused: Bool

    var body: some View {
        ZStack {
            Color.bg.ignoresSafeArea()
            VStack(spacing: 0) {
                Spacer()
                Image("Mark").resizable().frame(width: 96, height: 96).accessibilityHidden(true)
                Wordmark().padding(.bottom, 8)
                Text("P2P video player").font(.outfit(.subheadline)).foregroundStyle(Color.muted)
                    .padding(.bottom, 40)
                VStack(spacing: 12) {
                    TextField("", text: $input, prompt: Text("Content ID or acestream:// link").foregroundColor(.muted))
                        .font(.outfit(.body))
                        .foregroundStyle(Color.text)
                        .tint(.accent)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                        .submitLabel(.go)
                        .focused($focused)
                        .onSubmit(watch)
                        .padding(.horizontal, 14)
                        .frame(minHeight: 48)
                        .background(Color.panel, in: RoundedRectangle(cornerRadius: 8))
                        .overlay(RoundedRectangle(cornerRadius: 8)
                            .stroke(bad ? Color.err : focused ? Color.accent : Color.line, lineWidth: 1))
                    if bad {
                        Text("That is not a content ID or acestream:// link.")
                            .font(.outfit(.footnote)).foregroundStyle(Color.err)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    Button("Watch", action: watch).buttonStyle(AccentButton())
                }
                .frame(maxWidth: 420)
                Spacer()
                if let update {
                    Text("CanalPeer \(update) is available: update it in SideStore.")
                        .font(.outfit(.subheadline)).foregroundStyle(Color.text).multilineTextAlignment(.center)
                        .padding(12).frame(maxWidth: 420)
                        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.accent, lineWidth: 1))
                }
                Spacer()
                Text("Version \(appVersion)").font(.outfit(.footnote)).foregroundStyle(Color.muted)
            }
            .padding(24)
        }
        .onTapGesture { focused = false }
        // Only here, never in the middle of a channel.
        .task { update = await newerVersion() }
    }

    private func watch() {
        guard let id = normalize(input) else { bad = true; return }
        bad = false
        focused = false
        play(id)
    }
}

/// The SideStore source: viewers who added it get updates from SideStore
/// itself; the app reads it too, for those who installed the .ipa by hand.
private let sourceURL = URL(string: "https://canalpeer.com/app/ios-source.json")!

private struct Source: Decodable {
    struct App: Decodable { let bundleIdentifier: String; let versions: [Version] }
    struct Version: Decodable { let version: String }
    let apps: [App]
}

/// The newest published version when it is newer than this one; nil when not,
/// or when the site is unreachable (the app works the same without it). Looked
/// up by the published id: SideStore appends the viewer's team to the installed one.
func newerVersion() async -> String? {
    guard let (data, _) = try? await URLSession.shared.data(for: URLRequest(url: sourceURL, cachePolicy: .reloadIgnoringLocalCacheData)),
          let source = try? JSONDecoder().decode(Source.self, from: data),
          let latest = source.apps.first(where: { $0.bundleIdentifier == "com.canalpeer.app" })?.versions.first?.version
    else { return nil }
    return latest.compare(appVersion, options: .numeric) == .orderedDescending ? latest : nil
}
