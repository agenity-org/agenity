#if canImport(SwiftUI)
import SwiftUI
import ChepherdStyle
import ChepherdProtocol
import ChepherdTransport
import ChepherdAuth

struct DashboardView: View {
    @State private var store: SessionStore?
    @State private var selectedID: String?

    var body: some View {
        NavigationStack {
            List(selection: $selectedID) {
                Section {
                    if let store, !store.sessions.isEmpty {
                        ForEach(store.sessions) { s in
                            SessionRow(
                                session: s,
                                verdictHistory: store.verdicts
                                    .filter { $0.payload.session_uuid == s.uuid }
                                    .map { $0.payload }
                            )
                                .listRowBackground(Palette.background)
                                .tag(s.id)
                        }
                    } else {
                        Text(connectionStatusText)
                            .font(ChepherdFont.mono(ChepherdFont.sm))
                            .foregroundColor(Palette.timestamp)
                    }
                } header: {
                    HStack {
                        Text("SESSIONS")
                            .font(ChepherdFont.mono(ChepherdFont.sm, weight: .bold))
                            .foregroundColor(Palette.title)
                        Spacer()
                        if let store {
                            Text("\(store.transportKind.map { $0.rawValue } ?? "—")/\(store.transportState.rawValue)")
                                .font(ChepherdFont.mono(ChepherdFont.xs))
                                .foregroundColor(Palette.body)
                        }
                    }
                }
            }
            .listStyle(.plain)
            .background(Palette.background)
            .navigationDestination(item: $selectedID) { id in
                if let store, let session = store.sessions.first(where: { $0.id == id }) {
                    SessionDetailView(
                        session: session,
                        logs: store.logs,
                        verdictHistory: store.verdicts
                            .filter { $0.payload.session_uuid == session.uuid }
                            .map { $0.payload }
                    )
                }
            }
            .navigationTitle("chepherd")
        }
        .tint(Palette.logo)
        .task {
            await connectWithRetry()
        }
    }

    private func connectWithRetry() async {
        var attempt = 0
        while !Task.isCancelled {
            if store == nil || store?.transportState == .closed {
                store = nil
                await connectIfPossible()
                if store?.transportState == .open {
                    attempt = 0
                }
            }
            if store?.transportState == .open {
                attempt = 0
                try? await Task.sleep(nanoseconds: 5_000_000_000)
            } else {
                let delaySec = min(30.0, pow(2.0, Double(attempt)))
                attempt += 1
                try? await Task.sleep(nanoseconds: UInt64(delaySec * 1_000_000_000))
            }
        }
    }

    private func connectIfPossible() async {
        guard store == nil else { return }
        let tokenStore = TokenStore()
        guard let tokens = await tokenStore.load() else { return }
        let bastion = bastionFromJWT(tokens.accessToken) ?? "primary"
        let transport = WSTransport(
            url: URL(string: "wss://relay.chepherd.org/v1/signaling/ws")!,
            authToken: tokens.accessToken,
            bastionID: bastion
        )
        // HTTPSignaling reserved for the WebRTC factory wired in Wave 6.
        _ = HTTPSignaling(cfg: HTTPSignalingConfig(
            baseURL: URL(string: "https://relay.chepherd.org")!,
            authToken: tokens.accessToken
        ))
        let s = SessionStore(transport: transport)
        await s.connect()
        store = s
    }

    private func bastionFromJWT(_ jwt: String) -> String? {
        let parts = jwt.split(separator: ".")
        guard parts.count >= 2 else { return nil }
        var payload = String(parts[1])
        while payload.count % 4 != 0 { payload.append("=") }
        payload = payload.replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        guard let data = Data(base64Encoded: payload),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return nil
        }
        return (obj["chepherd_bastion"] as? String) ?? (obj["bid"] as? String)
    }

    private var connectionStatusText: String {
        guard let store else { return "preparing connection…" }
        switch store.transportState {
        case .idle, .connecting: return "connecting…"
        case .open: return "no sessions yet"
        case .closing, .closed: return "disconnected"
        }
    }
}

struct SessionDetailView: View {
    let session: SessionState
    var logs: [SessionStore.LogEntry] = []
    var verdictHistory: [VerdictPayload] = []

    var body: some View {
        VStack(alignment: .leading, spacing: ChepherdSpace.s4) {
            HStack(spacing: ChepherdSpace.s2) {
                BandDotView(band: session.trust_band?.rawValue, paused: session.paused)
                Text(session.tmux_name)
                    .font(ChepherdFont.mono(ChepherdFont.lg, weight: .bold))
                    .foregroundColor(Palette.primary)
                if let repo = session.repo {
                    Text(repo)
                        .font(ChepherdFont.mono(ChepherdFont.sm))
                        .foregroundColor(Palette.issueRef)
                }
            }
            if let s = session.last_scorecard {
                ScorecardView(scorecard: s)
            }
            if !verdictHistory.isEmpty, let s = session.last_scorecard {
                VStack(alignment: .leading, spacing: ChepherdSpace.s1) {
                    Text("trend")
                        .font(ChepherdFont.mono(ChepherdFont.sm, weight: .bold))
                        .foregroundColor(Palette.title)
                    sparklineRow("G", scoreSeries(\.G), current: s.G)
                    sparklineRow("V", scoreSeries(\.V), current: s.V)
                    sparklineRow("F", scoreSeries(\.F), current: s.F)
                    sparklineRow("E", scoreSeries(\.E), current: s.E)
                }
                .padding(ChepherdSpace.s3)
                .border(Palette.border, width: 1)
            }
            Text("log")
                .font(ChepherdFont.mono(ChepherdFont.base, weight: .bold))
                .foregroundColor(Palette.title)
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    let filtered = logs.filter { $0.session == session.tmux_name }.suffix(50)
                    ForEach(Array(filtered.enumerated()), id: \.offset) { _, l in
                        let levelPad = l.level.rawValue.padding(
                            toLength: 7, withPad: " ", startingAt: 0
                        )
                        Text("\(formatTime(l.at)) \(levelPad) \(l.text)")
                            .font(ChepherdFont.mono(ChepherdFont.sm))
                            .foregroundColor(Palette.body)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
            .background(Palette.background)
            .border(Palette.border, width: 1)
            Spacer()
        }
        .padding(ChepherdSpace.s4)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Palette.background)
    }

    private func formatTime(_ d: Date) -> String {
        let f = DateFormatter()
        f.dateFormat = "HH:mm:ss"
        return f.string(from: d)
    }

    private func scoreSeries(_ kp: KeyPath<Scorecard, Int>) -> [Int] {
        verdictHistory.compactMap { $0.scorecard?[keyPath: kp] }
    }

    @ViewBuilder
    private func sparklineRow(_ axis: String, _ series: [Int], current: Int) -> some View {
        HStack(spacing: ChepherdSpace.s2) {
            Text(axis)
                .font(ChepherdFont.mono(ChepherdFont.base, weight: .bold))
                .foregroundColor(Palette.title)
            SparklineView(values: series, current: current)
        }
    }
}
#endif
