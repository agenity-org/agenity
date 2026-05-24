#if canImport(SwiftUI)
import SwiftUI
import ChepherdStyle
import ChepherdProtocol

struct SessionRow: View {
    let session: SessionState
    var verdictHistory: [VerdictPayload] = []

    var body: some View {
        HStack(spacing: ChepherdSpace.s3) {
            BandDotView(band: session.trust_band?.rawValue, paused: session.paused)
            VStack(alignment: .leading, spacing: ChepherdSpace.s1) {
                Text(session.tmux_name)
                    .font(ChepherdFont.mono(ChepherdFont.base, weight: .bold))
                    .foregroundColor(Palette.primary)
                if let repo = session.repo {
                    Text(repo)
                        .font(ChepherdFont.mono(ChepherdFont.sm))
                        .foregroundColor(Palette.issueRef)
                }
            }
            Spacer()
            if let s = session.last_scorecard {
                Text("G\(s.G) V\(s.V) F\(s.F) E\(s.E)")
                    .font(ChepherdFont.mono(ChepherdFont.sm))
                    .foregroundColor(Palette.metric)
                    .accessibilityLabel("scorecard")
            }
            if let v = session.last_verdict {
                Text(v.rawValue)
                    .font(ChepherdFont.mono(ChepherdFont.sm))
                    .foregroundColor(Palette.verdictColor(v.rawValue))
            }
            if !verdictHistory.isEmpty {
                HistoryStripView(verdicts: verdictHistory)
            }
        }
        .padding(.vertical, ChepherdSpace.s2)
        .padding(.horizontal, ChepherdSpace.s3)
    }
}
#endif
