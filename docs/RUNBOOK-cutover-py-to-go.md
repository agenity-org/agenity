# Cutover: Python supervisor → Go daemon

Tracks chepherd/chepherd#16. Operator runbook for stopping the
legacy Python supervisor + starting the Go daemon.

## Pre-flight (no-op safety checks)

1. **Go daemon binary exists + builds**:
   ```bash
   cd ~/repos/chepherd
   go build -o /tmp/chepherd-go ./cmd
   /tmp/chepherd-go version  # should print 0.2.0-rc3
   ```

2. **Python supervisor PID known**:
   ```bash
   pgrep -f 'python.*supervisor.py'  # capture PID
   ```

3. **Tmux sessions Python is currently watching**:
   ```bash
   tmux ls 2>/dev/null | grep -E 'openova|iogrid|talentmesh|ping|vcard|cinova|kyc|api|termit'
   ```
   Count matches the number of sessions Python's `~/.local/share/chepherd-py/sessions.json` enumerates.

4. **Identity match — Go daemon's discovery sees the SAME sessions**:
   ```bash
   /tmp/chepherd-go status --json | jq '.sessions[] | .tmux_name' | sort
   tmux ls 2>/dev/null | awk -F: '{print $1}' | sort
   ```
   Both lists MUST match. Mismatch = discovery bug; abort cutover.

5. **Verdict equivalence on at least one live session**:
   ```bash
   # In a separate terminal, watch one session manually.
   # Compare Python's last 3 verdicts (cat ~/.local/state/chepherd-py/verdicts.jsonl | tail -3)
   # vs Go daemon's dry-run verdict on the same JSONL transcript.
   /tmp/chepherd-go judge --session iogrid-8 --dry-run
   ```
   G/V/F/E should differ by ≤1 axis and the verdict (silent/coach/intervene) MUST match exactly.

## Cutover (5-minute window)

```bash
# 1. Park Python supervisor (gracefully — SIGTERM, NOT SIGKILL).
kill -TERM "$(pgrep -f 'python.*supervisor.py')"

# 2. Wait for Python to flush its state file (≤2s typically).
sleep 3

# 3. Confirm Python is gone.
pgrep -f 'python.*supervisor.py' && {
  echo "FATAL: Python still running. Investigate before continuing."
  exit 1
}

# 4. Start Go daemon. Two options:
#
#    a) Foreground (recommended for first cutover — watch stdout live):
/tmp/chepherd-go daemon &
GO_PID=$!
#
#    b) Persistent via systemd (after foreground walk passes — survives
#       terminal exit + restarts on crash). One-time install:
#         mkdir -p ~/.config/systemd/user
#         cp ~/repos/chepherd/docs/systemd/chepherd-daemon.service \
#            ~/.config/systemd/user/
#         systemctl --user daemon-reload
#         systemctl --user enable --now chepherd-daemon
#         journalctl --user -u chepherd-daemon -f   # live log tail

# 5. Watch first 3 verdict cycles — should be silent/coach for healthy
#    sessions, intervene for any that drift. Compare to your mental
#    model of session state.
sleep 90  # one full cadence cycle (default 30s × 3 trusted sessions)

# 6. Check Go daemon's state file is populating.
ls -la ~/.local/state/chepherd-go/
```

## Rollback (if Go daemon misbehaves)

Single command — Python supervisor stays installed:

```bash
kill -TERM "$GO_PID"
sleep 3
~/repos/chepherd/run_supervisor.sh &  # restarts Python
```

State files don't conflict (different paths: `chepherd-py/` vs
`chepherd-go/`), so rollback is safe even mid-cycle.

## Post-cutover verification

After 30 minutes of Go daemon running:

- [ ] All tmux sessions still alive (none crashed under coaching)
- [ ] Go daemon's `~/.local/state/chepherd-go/verdicts.jsonl` has one
      entry per session per cadence tick
- [ ] No SUPERVISOR INJECT message in any tmux session beyond what the
      Go judge would have legitimately issued
- [ ] Founder-visible status string in TUI dashboard shows expected
      band per session (trusted = green dot, etc.)

When all 4 boxes ticked, retire Python supervisor:

```bash
# Move (don't delete) Python supervisor + state for 30-day grace period.
mv ~/repos/chepherd/supervisor.py ~/repos/chepherd/supervisor.py.retired-$(date +%Y%m%d)
mv ~/.local/state/chepherd-py ~/.local/state/chepherd-py.retired-$(date +%Y%m%d)
```

Update `chepherd/chepherd#16` with the cutover commit SHA + the
verdict-equivalence numbers from step 5.

## Risk window

The 5-minute cutover window is the only operator-attention period.
Pre-flight + post-verification are passive checks. Recommend cutover
during a quiet US/EU-evening window when the founder can monitor the
first cycle in the TUI.

## References

- chepherd/chepherd#16 — Cutover tracking issue
- chepherd/internal/daemon/judge — Go judge implementation
- chepherd/internal/daemon/band — adaptive trust band logic
- ~/repos/chepherd/supervisor.py — legacy Python supervisor
- chepherd/docs/PROTOCOL.md — wire shape (Go daemon's outputs match
  the protocol, so chepherd-rc clients see them identically)
