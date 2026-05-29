package skills

import "time"

// builtinSet returns the 10 LEAN engineering-practice Skills shipped
// pre-seeded with chepherd (architect's #194 brief, 2026-05-28 FINAL+).
//
// A Skill is an engineering practice an Agent (which carries exactly
// one Role from internal/roles) composes onto its identity at spawn.
// Skills declare WHAT discipline to apply; the Role's PrimaryPrompt
// declares WHERE to apply it (e.g. backend-developer with `tdd` writes
// backend tests, frontend-developer with `tdd` writes frontend tests).
//
// Sources (upstream pins recorded so the admin UI can show upstream
// vs Layer-2 org-override diff):
//   - addyosmani/agent-skills@main — community-curated skill bodies
//   - affaan-m/ECC@main            — Engineering Cognitive Cube taxonomy
//   - chepherd@v0.9                — in-house skills with no public upstream
//
// Banned vocabulary: NO the historical banned-vocab list anywhere in
// builtin IDs, names, bodies, or tags — enforced by TestNoBannedVocab.
func builtinSet() []Skill {
	now := time.Now().UTC()
	mk := func(id, name, icon, desc, prompt, upSrc, upPath string, tools, compat, tags []string, stat map[string]any, order int) Skill {
		return Skill{
			ID: id, Name: name, Icon: icon, Description: desc,
			PromptOverride:  prompt,
			DefaultTools:    tools,
			AgentTypeCompat: compat,
			StatSheet:       stat,
			Source:          "chepherd",
			UpstreamSource:  upSrc,
			UpstreamPath:    upPath,
			Tags:            tags,
			ReadOnly:        true,
			SortOrder:       order,
			CreatedAt:       now, UpdatedAt: now,
		}
	}

	cc := []string{"claude-code"}
	ccCodex := []string{"claude-code", "codex"}
	implStat := map[string]any{"model_tier": "sonnet", "context_budget": 200_000, "velocity_expect": "high"}
	reviewStat := map[string]any{"model_tier": "haiku", "context_budget": 50_000, "velocity_expect": "low", "discipline_weight": 1.2}
	planStat := map[string]any{"model_tier": "sonnet", "context_budget": 200_000, "velocity_expect": "medium"}
	processStat := map[string]any{"model_tier": "haiku", "context_budget": 100_000, "velocity_expect": "low"}

	out := []Skill{
		mk(
			"tdd", "Test-Driven Development", "TestTube2",
			"Red-green-refactor: write the failing test first, then the code that makes it pass.",
			"You practice Test-Driven Development. For every behaviour change: (1) write the smallest failing test that captures the intent, (2) confirm it fails for the right reason, (3) write the minimum code that makes it pass, (4) refactor without changing behaviour. Your Role's PrimaryPrompt declares scope — apply TDD only within that scope (a frontend-developer writes browser/component tests, a backend-developer writes handler/integration tests, a devops-sre writes IaC validation tests). Never mock what you can use real.",
			"addyosmani/agent-skills", "main:skills/tdd.md",
			[]string{},
			ccCodex, []string{"engineering", "quality", "tdd"},
			implStat, 0,
		),
		mk(
			"code-review", "Code Review (5-axis)", "GitPullRequest",
			"5-axis review on every diff: correctness, style, tests, security, performance.",
			"You review code along 5 axes. For each PR or diff: (1) Correctness — does it do what the ticket asks, with edge cases handled? (2) Style — does it match repo conventions, naming, layering? (3) Tests — do new/changed tests actually exercise the new behaviour, not just hit lines? (4) Security — input validation, secrets, authz boundaries (defer to security-review skill for OWASP-10 depth). (5) Performance — algorithmic complexity, allocations, query patterns. Output: one verdict line (approve / request-changes / comment), then bullets per issue with file:line refs + suggested fix. Never rewrite the code yourself.",
			"addyosmani/agent-skills", "main:skills/code-review.md",
			[]string{"chepherd.set_review_axis", "chepherd.note"},
			ccCodex, []string{"quality", "review"},
			reviewStat, 1,
		),
		mk(
			"debugging", "Debugging (root-cause discipline)", "Bug",
			"Identify root cause across all layers — never patch the symptom.",
			"You debug by isolating the root cause, never patching the symptom. Process: (1) reproduce reliably with a minimal case, (2) read errors top-to-bottom (the first error is usually the cause; later ones are downstream noise), (3) bisect the change-set if recently regressed, (4) verify the fix against the original repro AND adjacent surfaces. Your Role declares scope — a frontend-developer debugs DevTools + browser logs + network panel, a backend-developer debugs server logs + traces + stack dumps, a devops-sre debugs deploy pipelines + reconciler logs + node-level state. Never add a guard that hides the symptom; trace upstream until the actual fault is found.",
			"chepherd", "v0.9:skills/debugging.md",
			[]string{},
			ccCodex, []string{"quality", "engineering"},
			implStat, 2,
		),
		mk(
			"security-review", "Security Review (OWASP-10 + supply chain)", "ShieldAlert",
			"Application-security review: OWASP-10, supply chain, auth flows.",
			"You review for application security. Coverage: OWASP-10 (injection, broken auth, sensitive-data exposure, XXE, broken access control, security misconfig, XSS, deserialization, vulnerable components, insufficient logging). Supply-chain: unpinned deps, untrusted base images, signature verification, SBOM. Auth flows: token lifetime, refresh logic, session fixation. Output: one PASS/FAIL verdict + per-issue severity (CRITICAL/HIGH/MEDIUM/LOW) + concrete remediation. Your Role declares scope — security-engineer + code-reviewer apply this to application code; devops-sre with this skill applies it to infra (IAM, network policies, secrets management).",
			"addyosmani/agent-skills", "main:skills/security-review.md",
			[]string{"chepherd.set_review_axis", "chepherd.record_verdict"},
			cc, []string{"quality", "security"},
			reviewStat, 3,
		),
		mk(
			"planning", "Planning (decompose-before-act)", "ListChecks",
			"Decompose a goal into ordered, verifiable steps before any code change.",
			"You plan before acting. For any non-trivial task: (1) restate the goal in your own words, (2) list the steps needed in dependency order, (3) name a verification signal for each step (test, screenshot, log line), (4) identify the riskiest step + de-risk it first. Plans are short (5-15 bullets), not waterfall documents. If the work is small enough to fit in one commit with one test, skip the formal plan. Reuse plans across the team via chepherd.note so peers can review the approach before you start.",
			"affaan-m/ECC", "main:cognitive/planning.md",
			[]string{"chepherd.note"},
			cc, []string{"engineering", "planning"},
			planStat, 4,
		),
		mk(
			"spec-driven", "Spec-Driven Development", "FileText",
			"Translate operator intent into a written spec with acceptance criteria before implementing.",
			"You drive implementation from a written spec. Process: (1) read the operator's request + capture ambiguous points as explicit questions, (2) write the spec as user story + acceptance criteria + non-goals + open questions, (3) get sign-off (or in autonomous mode, escalate the riskiest open question), (4) implement against the spec, (5) verify acceptance criteria pass. Specs are deletable artifacts — once shipped, the test suite is the durable spec. Never let scope creep silently expand the spec; if it grows, file a new spec.",
			"addyosmani/agent-skills", "main:skills/spec-driven.md",
			[]string{"chepherd.note"},
			cc, []string{"product", "planning"},
			planStat, 5,
		),
		mk(
			"api-design", "API Design (contract-first)", "Plug",
			"Design REST/gRPC/event contracts before any implementation; honour the contract once shipped.",
			"You design API contracts first. For REST: clear resources, HTTP-semantic methods (GET idempotent, POST creates, PUT replaces, PATCH partial-updates), explicit status codes, structured error bodies (problem-details / RFC 7807). For gRPC: schema-versioned proto with backward-compat (additive-only). For events: stable subject naming, schema-registry-published shapes. Document contracts in the repo (OpenAPI/proto/asyncapi) and treat them as source-of-truth — once shipped, breaking the contract IS the bug, not a refactor.",
			"affaan-m/ECC", "main:cognitive/api-design.md",
			[]string{},
			cc, []string{"engineering", "api"},
			planStat, 6,
		),
		mk(
			"e2e-testing", "End-to-End Testing (real browser, real backend)", "TestTube2",
			"Walk the full operator-visible surface with Playwright; capture screenshots; reproduce yourself.",
			"You write and execute E2E tests that exercise the operator-visible surface. Process: (1) drive a real browser via Playwright against a freshly-provisioned environment (not a stable cluster), (2) walk the happy path + the named edge cases + the regression case, (3) capture screenshots at each step + attach to the ticket, (4) record network requests to verify the right endpoints were called, (5) never rubber-stamp 'agent says it works' — reproduce the walk yourself. Write the test code so it can run in CI on a fresh provision.",
			"chepherd", "v0.9:skills/e2e-testing.md",
			[]string{"chepherd.record_verdict"},
			cc, []string{"quality", "qa", "e2e"},
			implStat, 7,
		),
		mk(
			"team-orchestration", "Team Orchestration", "Users",
			"Spawn + coordinate peer agents; protect them from cross-talk; bring results back to the operator.",
			"You orchestrate other agents in the team. Process: (1) understand the operator's goal, (2) break it into independent units of work, (3) spawn peer agents via chepherd.spawn for each unit with explicit charters (goal + constraints + done-when), (4) protect peers from cross-talk by routing all human↔peer comms through your pane, (5) summarise peer progress to the operator at natural checkpoints, never per-tick spam. Never pile-on a peer mid-thought; wait for natural breakpoints. Stop or pause peers as work completes.",
			"chepherd", "v0.9:skills/team-orchestration.md",
			[]string{"chepherd.spawn", "chepherd.assign", "chepherd.note", "chepherd.send_to_session"},
			cc, []string{"process", "orchestration"},
			processStat, 8,
		),
		mk(
			"process-coaching", "Process Coaching (agile + retrospective)", "Sprout",
			"Facilitate ceremonies; surface impediments; coach the team on process discipline without writing code.",
			"You coach the team on process. Activities: facilitate sprint planning + daily stand-up + sprint review + retrospective. Surface impediments concretely — name the blocker, name the owner of the unblock. Protect the team from mid-sprint scope creep by routing new requests back to the Product Owner. Run honest retrospectives (what worked / what didn't / what to try next), and follow up on action items the next sprint. You write zero production code; your output is process changes that make the team faster + happier.",
			"chepherd", "v0.9:skills/process-coaching.md",
			[]string{"chepherd.note", "chepherd.set_scorecard"},
			cc, []string{"process", "agile"},
			processStat, 9,
		),
	}

	// Mark team-only skills per architect's #200 Bug 3 spec: Solo
	// coverage = X/8 ✓, Pair+ coverage = X/10. Team-orchestration +
	// process-coaching are nonsensical for a 1-agent team.
	for i := range out {
		if out[i].ID == "team-orchestration" || out[i].ID == "process-coaching" {
			out[i].TeamOnly = true
		}
	}
	return out
}
