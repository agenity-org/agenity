#if canImport(SwiftUI)
import SwiftUI
import ChepherdStyle
import ChepherdProtocol

struct HistoryStripView: View {
    let verdicts: [VerdictPayload]
    var limit: Int = 12

    var body: some View {
        HStack(spacing: 1) {
            ForEach(verdicts.suffix(limit).indices, id: \.self) { i in
                let v = verdicts.suffix(limit)[i]
                Text("●")
                    .font(ChepherdFont.mono(ChepherdFont.sm))
                    .foregroundColor(Palette.verdictColor(v.verdict.rawValue))
                    .accessibilityHidden(true)
            }
            if verdicts.isEmpty {
                Text("—")
                    .font(ChepherdFont.mono(ChepherdFont.xs))
                    .foregroundColor(Palette.timestamp)
            }
        }
        .accessibilityLabel("verdict history")
    }
}
#endif
