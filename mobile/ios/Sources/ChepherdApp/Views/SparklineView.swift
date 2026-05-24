#if canImport(SwiftUI)
import SwiftUI
import ChepherdStyle

// 8-cell ▁▂▃▄▅▆▇█ trend bar with per-cell band colour.
// Mirrors chepherd-rc-web/src/components/Sparkline.svelte and
// chepherd-rc-android/app/.../ui/Sparkline.kt.

struct SparklineView: View {
    let values: [Int]
    var current: Int?

    private let glyphs = ["▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"]

    var body: some View {
        HStack(spacing: 0) {
            let trailing = Array(values.suffix(8))
            ForEach(trailing.indices, id: \.self) { i in
                Text(glyphFor(trailing[i]))
                    .font(ChepherdFont.mono(ChepherdFont.base))
                    .foregroundColor(bandFor(trailing[i]))
                    .accessibilityHidden(true)
            }
            if let c = current {
                Text("\(c)")
                    .font(ChepherdFont.mono(ChepherdFont.base, weight: .bold))
                    .foregroundColor(bandFor(c))
                    .padding(.leading, ChepherdSpace.s1)
                    .accessibilityLabel("current \(c)")
            }
        }
    }

    private func glyphFor(_ v: Int) -> String {
        let clamped = max(0, min(10, v))
        let idx = Int(Double(clamped) / 10.0 * Double(glyphs.count - 1))
        return glyphs[idx]
    }

    private func bandFor(_ v: Int) -> Color {
        switch v {
        case ...3: return Palette.bandCrisis
        case 4...6: return Palette.bandConcerned
        default: return Palette.bandTrusted
        }
    }
}
#endif
