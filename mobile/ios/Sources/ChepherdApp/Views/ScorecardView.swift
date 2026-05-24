#if canImport(SwiftUI)
import SwiftUI
import ChepherdStyle
import ChepherdProtocol

struct ScorecardView: View {
    let scorecard: Scorecard

    var body: some View {
        VStack(alignment: .leading, spacing: ChepherdSpace.s1) {
            row("G", "goal", scorecard.G)
            row("V", "velocity", scorecard.V)
            row("F", "focus", scorecard.F)
            row("E", "end-state", scorecard.E)
        }
        .padding(ChepherdSpace.s3)
        .background(Palette.background)
        .border(Palette.border, width: 1)
    }

    @ViewBuilder
    private func row(_ axis: String, _ label: String, _ value: Int) -> some View {
        HStack(spacing: ChepherdSpace.s2) {
            Text(axis)
                .font(ChepherdFont.mono(ChepherdFont.base, weight: .bold))
                .foregroundColor(Palette.title)
            Text(label)
                .font(ChepherdFont.mono(ChepherdFont.base))
                .foregroundColor(Palette.body)
                .frame(width: 100, alignment: .leading)
            Text(": \(value) / 10")
                .font(ChepherdFont.mono(ChepherdFont.base, weight: .bold))
                .foregroundColor(bandFor(value))
            Text(gaugeBar(value))
                .font(ChepherdFont.mono(ChepherdFont.base))
                .foregroundColor(bandFor(value))
                .accessibilityHidden(true)
        }
    }

    private func gaugeBar(_ v: Int) -> String {
        let filled = max(0, min(10, v))
        return String(repeating: "▰", count: filled) + String(repeating: "▱", count: 10 - filled)
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
