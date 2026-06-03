# agentpatterns testdata

Per-flavor PTY-bytes fixtures consumed by the unit tests in
`agentpatterns_test.go`. The fixtures must be RECORDED from real
CLI agent binaries, not synthesized — minimal repros have
historically drifted from the binaries that ship to operators
([[feedback_real_fixtures_not_minimal_repro]]).

## Fixture provenance

| File | Capture command | Binary version |
|---|---|---|
| `claude_code_print_json_endturn.json` | `claude --print --output-format json "say only the word 'ack' and nothing else"` | claude-code 2.1.148 (captured 2026-05-31 on this build host) |
| `qwen_code_prompt_idle.txt` | (synthesized — no live qwen-code binary on this build host; documented prompt shape per qwen-code README) | (to-be-recaptured once a live binary is available on a build host) |
| `aider_prompt_idle.txt` | (synthesized — no live aider binary on this build host; documented prompt shape per aider docs) | (to-be-recaptured once a live binary is available on a build host) |

## When to regenerate

- A flavor's binary releases a TUI redesign (prompt glyph
  change, ANSI escape sequence change). Recapture against the
  new version + bump the version column above.
- A flavor's detector fires a false-positive or false-negative
  on real operator traffic. Reproduce against the actual bytes
  + record the failing fixture here before tuning the detector.

## When NOT to regenerate

- Detector tests fail. Tests pin the contract; don't change the
  fixture to make a broken detector pass.
- A "cleaner" version of the fixture seems easier to read. The
  bytes are deliberately raw — they must match what the binary
  produced. Cleanup defeats the whole point of real-fixture
  testing.
