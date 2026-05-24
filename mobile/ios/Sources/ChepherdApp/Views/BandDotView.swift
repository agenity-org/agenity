#if canImport(SwiftUI)
import SwiftUI
import ChepherdStyle

struct BandDotView: View {
    let band: String?
    var paused: Bool = false

    var body: some View {
        Text(paused ? "○" : "●")
            .font(ChepherdFont.mono(ChepherdFont.base))
            .foregroundColor(Palette.bandColor(band, paused: paused))
            .accessibilityLabel(paused ? "paused" : (band ?? "standard"))
    }
}
#endif
