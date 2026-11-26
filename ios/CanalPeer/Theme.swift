import SwiftUI

/// The site's palette and type (canalpeer-web/docs/brand.md).
extension Color {
    static let bg = Color(hex: 0x0f1115)
    static let panel = Color(hex: 0x16191f)
    static let line = Color(hex: 0x2b313a)
    static let text = Color(hex: 0xe8ebef)
    static let ink = Color(hex: 0xf3f5f7)
    static let muted = Color(hex: 0xadb4be)
    static let accent = Color(hex: 0x5fb8a0)
    static let onAccent = Color(hex: 0x0b1512)
    static let err = Color(hex: 0xe5677f)

    init(hex: UInt32) {
        self.init(red: Double(hex >> 16 & 0xff) / 255, green: Double(hex >> 8 & 0xff) / 255, blue: Double(hex & 0xff) / 255)
    }
}

extension Font {
    /// Outfit at a Dynamic Type style, so it follows the viewer's text size.
    static func outfit(_ style: TextStyle, _ weight: Weight = .regular) -> Font {
        let name = switch weight {
        case .bold, .heavy, .black: "Outfit-Bold"
        case .semibold, .medium: "Outfit-SemiBold"
        default: "Outfit-Regular"
        }
        return .custom(name, size: UIFont.preferredFont(forTextStyle: style.uiKit).pointSize, relativeTo: style)
    }
}

private extension Font.TextStyle {
    var uiKit: UIFont.TextStyle {
        switch self {
        case .largeTitle: .largeTitle
        case .title: .title1
        case .title2: .title2
        case .title3: .title3
        case .headline: .headline
        case .subheadline: .subheadline
        case .callout: .callout
        case .footnote: .footnote
        case .caption: .caption1
        case .caption2: .caption2
        default: .body
        }
    }
}

/// The logotype: "canal" regular, "peer" bold in the accent.
struct Wordmark: View {
    var body: some View {
        (Text("canal").font(.outfit(.largeTitle)).foregroundColor(.ink)
            + Text("peer").font(.outfit(.largeTitle, .bold)).foregroundColor(.accent))
            .accessibilityLabel("CanalPeer")
    }
}

/// The site's primary button: accent fill, dark label.
struct AccentButton: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.outfit(.body, .semibold))
            .foregroundStyle(Color.onAccent)
            .frame(maxWidth: .infinity, minHeight: 48)
            .background(Color.accent.opacity(configuration.isPressed ? 0.8 : 1), in: RoundedRectangle(cornerRadius: 8))
    }
}
