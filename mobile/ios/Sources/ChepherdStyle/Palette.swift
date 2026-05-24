// chepherd design palette — Swift mirror of:
//   chepherd/internal/style/palette.go (Go canon)
//   chepherd-rc-web/src/styles/tokens.css (Web mirror)
//   chepherd/docs/DESIGN-SYSTEM.md (human-readable contract)
//
// Any drift between this file and the canon is a design bug.

#if canImport(SwiftUI)
import SwiftUI

public enum Palette {
    // §2.1 Body
    public static let body       = Color(hex: 0x5F9EA0) // cadetblue
    public static let primary    = Color(hex: 0xFFFFFF) // white
    public static let background = Color(hex: 0x000000) // black

    // §2.2 Brand
    public static let logo = Color(hex: 0xFFA500) // orange

    // §2.3 Frame chrome
    public static let title       = Color(hex: 0x00FFFF) // aqua
    public static let titleRule   = Color(hex: 0x87CEFA) // lightskyblue
    public static let border      = Color(hex: 0x1E90FF) // dodgerblue
    public static let borderFocus = Color(hex: 0x87CEFA) // lightskyblue

    // §2.4 Menu / footer
    public static let keyLetter  = Color(hex: 0x1E90FF) // dodgerblue
    public static let keyDesc    = Color(hex: 0xFFFFFF) // white
    public static let keyNumeric = Color(hex: 0xFF00FF) // fuchsia

    // §2.5 Breadcrumbs
    public static let crumbFG     = Color(hex: 0x000000)
    public static let crumbBG     = Color(hex: 0x4682B4) // steelblue
    public static let crumbActive = Color(hex: 0xFFA500) // orange

    // §2.6 Trust bands
    public static let bandTrusted   = Color(hex: 0xADFF2F) // greenyellow
    public static let bandStandard  = Color(hex: 0x5F9EA0) // cadetblue
    public static let bandConcerned = Color(hex: 0xFF8C00) // darkorange
    public static let bandCrisis    = Color(hex: 0xFF4500) // orangered
    public static let bandPaused    = Color(hex: 0x778899) // lightslategray

    // §2.7 Verdict
    public static let verdictSilent    = Color(hex: 0x5F9EA0)
    public static let verdictPraise    = Color(hex: 0xADFF2F)
    public static let verdictCoach     = Color(hex: 0xFF8C00)
    public static let verdictIntervene = Color(hex: 0xFF4500)

    // §2.8 Special events
    public static let injected    = Color(hex: 0x9370DB) // mediumpurple
    public static let escalating  = Color(hex: 0xFFEFD5) // papayawhip
    public static let apiError    = Color(hex: 0xFF4500) // orangered
    public static let adopted     = Color(hex: 0x00CED1) // darkturquoise

    // §2.9 Metrics + refs
    public static let metric     = Color(hex: 0xFFEFD5) // papayawhip
    public static let issueRef   = Color(hex: 0x4682B4) // steelblue
    public static let marked     = Color(hex: 0xB8860B) // darkgoldenrod
    public static let timestamp  = Color(hex: 0x778899) // lightslategray

    /// Look up the band colour by enum so callers don't switch on strings.
    public static func bandColor(_ band: String?, paused: Bool = false) -> Color {
        if paused { return bandPaused }
        switch band {
        case "trusted":   return bandTrusted
        case "concerned": return bandConcerned
        case "crisis":    return bandCrisis
        case "paused":    return bandPaused
        default:          return bandStandard
        }
    }

    public static func verdictColor(_ v: String?) -> Color {
        switch v {
        case "praise":    return verdictPraise
        case "coach":     return verdictCoach
        case "intervene": return verdictIntervene
        default:          return verdictSilent
        }
    }
}

public extension Color {
    /// Init from a hex constant like 0xFFA500.
    init(hex: UInt32) {
        let r = Double((hex >> 16) & 0xFF) / 255.0
        let g = Double((hex >> 8) & 0xFF) / 255.0
        let b = Double(hex & 0xFF) / 255.0
        self.init(.sRGB, red: r, green: g, blue: b, opacity: 1.0)
    }
}
#endif
