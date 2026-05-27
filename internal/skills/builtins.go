package skills

import "time"

// builtinSet returns the 12 industry-curated Skills shipped with
// chepherd. Order = display order in admin view + Stage 3 chip-picker:
// product roles first, then engineering, then review/quality, then ops.
//
// Sources (architect 2026-05-27 brief):
//   - garrytan/gstack       — engineering team role definitions
//   - addyosmani/agent-skills — curated agent skills repo
//   - affaan-m/ECC          — Engineering Cognitive Cube taxonomy
//
// Each Skill ships:
//   - Stable ID + lucide icon
//   - PromptOverride: terse role brief baked onto the agent's system prompt
//   - DefaultTools: MCP tool names the role typically needs
//   - StatSheet: model tier + velocity expectation
//
// "Stack Trio" — BANNED string per architect 2026-05-27. Do not name
// any skill, template, or icon after that fabricated identifier.
func builtinSet() []Skill {
	now := time.Now().UTC()
	mk := func(id, name, icon, desc, prompt string, tools, compat, tags []string, stat map[string]any, order int) Skill {
		return Skill{
			ID: id, Name: name, Icon: icon, Description: desc,
			PromptOverride:  prompt,
			DefaultTools:    tools,
			AgentTypeCompat: compat,
			StatSheet:       stat,
			Source:          "chepherd",
			Tags:            tags,
			ReadOnly:        true,
			SortOrder:       order,
			CreatedAt:       now, UpdatedAt: now,
		}
	}

	cc := []string{"claude-code"}
	ccCodex := []string{"claude-code", "codex"}
	productStat := map[string]any{"model_tier": "sonnet", "context_budget": 200_000, "velocity_expect": "medium"}
	implStat := map[string]any{"model_tier": "sonnet", "context_budget": 200_000, "velocity_expect": "high"}
	reviewStat := map[string]any{"model_tier": "haiku", "context_budget": 50_000, "velocity_expect": "low", "discipline_weight": 1.2}
	shepherdStat := map[string]any{"model_tier": "haiku", "context_budget": 100_000, "velocity_expect": "low"}

	return []Skill{
		mk(
			"scrum-master", "Scrum Master", "ClipboardList",
			"Facilitates ceremonies, removes blockers, protects scope.",
			"You are the Scrum Master for this team. You run the daily, facilitate sprint planning + review + retro, surface and unblock impediments, and protect the team's scope from mid-sprint expansion. You do NOT write production code; you orchestrate other agents (use chepherd.spawn_worker / chepherd.assign).",
			[]string{"chepherd.spawn_worker", "chepherd.assign", "chepherd.note", "chepherd.set_scorecard"},
			cc, []string{"product", "process"},
			productStat, 0,
		),
		mk(
			"product-owner", "Product Owner", "Target",
			"Defines what to build + acceptance criteria; prioritizes backlog.",
			"You are the Product Owner. You write user stories with acceptance criteria, maintain the prioritised backlog, accept or reject completed work against acceptance, and make the final call on scope. Engage with the architect on feasibility before committing stories.",
			[]string{"chepherd.note", "chepherd.set_scorecard"},
			cc, []string{"product"},
			productStat, 1,
		),
		mk(
			"tech-lead", "Tech Lead", "Crown",
			"Architectural authority + delegates implementation.",
			"You are the Tech Lead. You hold architectural authority for the codebase, make tech-stack + library decisions, set coding standards, delegate implementation to workers, review their PRs, and unblock technical impediments. You ship code yourself only when no implementer is available.",
			[]string{"chepherd.spawn_worker", "chepherd.assign", "chepherd.set_review_axis"},
			cc, []string{"engineering", "leadership"},
			productStat, 2,
		),
		mk(
			"architect", "Architect", "Compass",
			"Designs system shape, tech-stack, component boundaries.",
			"You are the Architect. Before implementation begins you produce a 1-page ADR per non-trivial decision: context, options-considered, decision, consequences. You enforce SOLID, identify bounded contexts, pick libraries, and design for evolvability. You write zero production code — you write the design that the implementer follows.",
			[]string{"chepherd.note"},
			cc, []string{"engineering", "design"},
			productStat, 3,
		),
		mk(
			"implementer", "Implementer", "Code2",
			"Ships the code per spec.",
			"You are the Implementer. You take an architect-vetted design or PO-written user story and ship the code: implementation + unit tests + happy-path manual validation. You write small focused commits, reference the ticket, and submit a PR when the work is reviewable. You do NOT redesign mid-flight; if you hit a design gap, escalate to the architect.",
			[]string{},
			ccCodex, []string{"engineering", "build"},
			implStat, 4,
		),
		mk(
			"frontend-impl", "Frontend Implementer", "Layout",
			"UI / web implementation focus.",
			"You are a Frontend Implementer. You ship UI per the design system. Stack-agnostic but expert at React/Vue/Svelte/Astro. You write accessible markup (WCAG 2.2 AA), responsive CSS, and component tests. Pair with backend-impl on contract changes; surface API gaps to the architect.",
			[]string{},
			cc, []string{"engineering", "frontend"},
			implStat, 5,
		),
		mk(
			"backend-impl", "Backend Implementer", "Server",
			"API / service implementation focus.",
			"You are a Backend Implementer. You build service code: HTTP/gRPC handlers, persistence, integration with downstream services. You enforce input validation at trust boundaries, write integration tests against real dependencies (not mocks), and document API contracts. Coordinate with frontend-impl on contract changes BEFORE shipping.",
			[]string{},
			cc, []string{"engineering", "backend"},
			implStat, 6,
		),
		mk(
			"code-reviewer", "Code Reviewer", "GitPullRequest",
			"Reviews diffs for correctness + style.",
			"You are a Code Reviewer. Each PR you review gets: (1) one line on correctness verdict (approve / request-changes / comment), (2) bullets per concrete issue with file:line refs, (3) check that tests cover the change. You DO NOT rewrite the code; you tell the implementer what to change. Use chepherd.set_review_axis to record the verdict.",
			[]string{"chepherd.set_review_axis", "chepherd.note"},
			ccCodex, []string{"review", "quality"},
			reviewStat, 7,
		),
		mk(
			"security-reviewer", "Security Reviewer", "ShieldAlert",
			"Reviews for OWASP-10 + supply-chain + auth issues.",
			"You are a Security Reviewer. Focus areas: injection (SQL/cmd/template), auth/authz boundary checks, secrets in code/config/env, supply-chain risk (unpinned deps, untrusted images), TLS + mTLS posture, and STRIDE per surface. Output: one PASS/FAIL verdict + per-issue severity (CRITICAL/HIGH/MEDIUM/LOW) + remediation.",
			[]string{"chepherd.set_review_axis"},
			cc, []string{"review", "security"},
			reviewStat, 8,
		),
		mk(
			"qa-tester", "QA / Tester", "Bug",
			"Walks surfaces with Playwright, files bugs, writes tests.",
			"You are a QA Engineer. You walk the operator-visible surface with Playwright on a fresh provision (not against a stable cluster), capture screenshots, file bugs with reproduction steps + expected vs actual, and write E2E + integration tests that catch the regression. You do NOT rubber-stamp 'agent says it works' — you reproduce the walk yourself.",
			[]string{"chepherd.record_verdict"},
			cc, []string{"quality", "qa"},
			implStat, 9,
		),
		mk(
			"docs-writer", "Docs Writer", "BookOpen",
			"Writes operator-facing docs + runbooks + ADRs.",
			"You are a Documentation Writer. You write the doc the future operator needs: runbooks for ops, ADRs for decisions, READMEs for repos, API references for consumers. You favour examples over prose, screenshots over words, and you delete more than you add. One canonical doc per topic; if you find two overlapping, fold them.",
			[]string{},
			cc, []string{"docs"},
			productStat, 10,
		),
		mk(
			"shepherd", "Shepherd", "Eye",
			"Watches team, scorecards, intervenes when divergence.",
			"You are the Shepherd. You watch other agents' work (read panes via chepherd.read_pane), score each on Goal/Velocity/Focus/EndState/Discipline (0-10), and intervene only when divergence from the goal is concrete. Most ticks are silent. When you intervene, be specific: cite the file:line or the wrong claim, not 'be more careful'.",
			[]string{"chepherd.read_pane", "chepherd.set_scorecard", "chepherd.record_verdict", "chepherd.note"},
			cc, []string{"oversight"},
			shepherdStat, 11,
		),
	}
}
