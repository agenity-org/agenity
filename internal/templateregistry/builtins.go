package templateregistry

import "time"

// builtinSet returns the 6 visible Stage-1 templates + 3 hidden
// admin-only templates. Architect 2026-05-28 FINAL+ amendment.
//
// Visible (display order = sort_order ascending; 2×3 grid in Stage 1):
//   0. solo   (size 1, Fibonacci)
//   1. pair   (size 2, Fibonacci)
//   2. trio   (size 3, Fibonacci)
//   3. scrum  (size 5, Fibonacci)
//   4. squad  (size 8, Fibonacci)
//   5. custom (size 0, operator-composed)
//
// Hidden (admin-only; not in Stage 1 grid):
//   100. solo-supervised
//   101. council
//   102. multi-team
//
// Every Slot references:
//   - RoleID from internal/roles (12 builtins)
//   - OwnedSkills from internal/skills (10 LEAN)
//   - OwnedSkillsScope when the role+skill pair is ambiguous
//
// Banned vocabulary check: no "shepherd" / "Stack Trio" / "RACI"
// anywhere in IDs, names, descriptions, slot labels, or whenToUse —
// enforced by TestNoBannedVocab in registry_test.go.
func builtinSet() []Template {
	now := time.Now().UTC()

	mk := func(label, roleID string, owned []string, scope map[string]string) Slot {
		return Slot{
			Label: label, RoleID: roleID,
			OwnedSkills:         owned,
			OwnedSkillsScope:    scope,
			AgentTypeDefault:    "claude-code",
			AccountClassDefault: "anthropic",
		}
	}

	return []Template{
		{
			ID: "solo", Name: "Solo", Icon: "User",
			Description: "One agent, every discipline. Exploration / quick spikes / personal projects.",
			WhenToUse:   "Daily defaults; experimenting with a fresh repo; one-shot fixes.",
			SizeLabel:   "1", SortOrder: 0, Visible: true,
			Slots: []Slot{
				mk("solo-1", "generalist",
					[]string{"planning", "spec-driven", "tdd", "debugging", "code-review", "security-review", "api-design", "e2e-testing"},
					map[string]string{"tdd": "all", "debugging": "all"}),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "pair", Name: "Pair", Icon: "Users",
			Description: "XP pair-programming — one builds end-to-end, one reviews + coaches.",
			WhenToUse:   "Two-set-of-eyes problems; mentoring; live review; small teams without dedicated leadership.",
			SizeLabel:   "2", SortOrder: 1, Visible: true,
			Slots: []Slot{
				mk("impl-1", "full-stack-developer",
					[]string{"tdd", "debugging", "planning", "spec-driven", "api-design", "e2e-testing"},
					map[string]string{"tdd": "all"}),
				// Pair-conditional: code-reviewer absorbs team-orchestration +
				// process-coaching in a 2-person team (no dedicated Scrum
				// Master / Tech Lead present). Architect 2026-05-28.
				mk("reviewer-1", "code-reviewer",
					[]string{"code-review", "security-review", "team-orchestration", "process-coaching"},
					nil),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "trio", Name: "Trio", Icon: "Network",
			Description: "Smallest balanced team: lead drives + implementer ships + reviewer checks.",
			WhenToUse:   "Focused feature work needing design + ship + check; small but disciplined.",
			SizeLabel:   "3", SortOrder: 2, Visible: true,
			Slots: []Slot{
				mk("lead-1", "tech-lead",
					[]string{"planning", "code-review", "team-orchestration", "process-coaching"},
					nil),
				mk("impl-1", "full-stack-developer",
					[]string{"tdd", "debugging", "spec-driven", "api-design", "e2e-testing"},
					map[string]string{"tdd": "all"}),
				mk("reviewer-1", "code-reviewer",
					[]string{"code-review", "security-review"},
					nil),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "scrum", Name: "Scrum Team", Icon: "KanbanSquare",
			Description: "Full scrum cadence — product owner, methodology coach, design + build + QA.",
			WhenToUse:   "Features needing upstream design review + downstream QA sign-off; sprint cadence.",
			SizeLabel:   "5", SortOrder: 3, Visible: true,
			Slots: []Slot{
				mk("po-1", "product-owner",
					[]string{"spec-driven", "planning"},
					nil),
				mk("sm-1", "scrum-master",
					[]string{"team-orchestration", "process-coaching"},
					nil),
				mk("arch-1", "architect",
					[]string{"planning", "api-design", "security-review", "code-review"},
					nil),
				mk("impl-1", "full-stack-developer",
					[]string{"tdd", "debugging", "api-design", "e2e-testing"},
					map[string]string{"tdd": "all"}),
				mk("qa-1", "qa-engineer",
					[]string{"e2e-testing"},
					nil),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "squad", Name: "Squad", Icon: "Layers3",
			Description: "Full multi-discipline product squad: leadership + FE/BE/DevOps split + QA + security + review.",
			WhenToUse:   "Production-grade features across the stack; clear separation of frontend/backend/infra concerns.",
			SizeLabel:   "8", SortOrder: 4, Visible: true,
			Slots: []Slot{
				mk("po-1", "product-owner",
					[]string{"spec-driven", "planning"},
					nil),
				mk("sm-1", "scrum-master",
					[]string{"team-orchestration", "process-coaching"},
					nil),
				mk("arch-1", "architect",
					[]string{"planning", "api-design", "security-review", "code-review"},
					nil),
				mk("fe-1", "frontend-developer",
					[]string{"tdd", "debugging"},
					map[string]string{"tdd": "frontend", "debugging": "frontend"}),
				mk("be-1", "backend-developer",
					[]string{"tdd", "debugging", "api-design"},
					map[string]string{"tdd": "backend", "debugging": "backend"}),
				mk("ops-1", "devops-sre",
					[]string{"tdd", "security-review"},
					map[string]string{"tdd": "iac", "security-review": "infra"}),
				mk("qa-1", "qa-engineer",
					[]string{"e2e-testing"},
					nil),
				mk("sec-1", "security-engineer",
					[]string{"security-review"},
					map[string]string{"security-review": "application"}),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "custom", Name: "Custom", Icon: "PlusCircle",
			Description: "Build your own team — pick roles, attach skills, set count.",
			WhenToUse:   "You know exactly which roles you need; want to deviate from the standard shapes.",
			SizeLabel:   "0", SortOrder: 5, Visible: true,
			Slots:       []Slot{}, // empty — operator composes in Stage 3
			ReadOnly:    true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},

		// ─── Hidden / admin-only builtins ────────────────────────────
		// Operator can flip Visible=true via /admin/templates to re-enable
		// these in the Stage 1 grid.

		{
			ID: "solo-supervised", Name: "Solo (Supervised)", Icon: "UserCheck",
			Description: "Generalist + tech-lead supervisor — continuous discipline scoring on a single driver.",
			WhenToUse:   "Single-driver work where you want a second-pair-of-eyes audit per commit.",
			SizeLabel:   "2", SortOrder: 100, Visible: false,
			Slots: []Slot{
				mk("solo-1", "generalist",
					[]string{"planning", "spec-driven", "tdd", "debugging", "code-review", "api-design", "e2e-testing"},
					map[string]string{"tdd": "all"}),
				mk("supervisor-1", "tech-lead",
					[]string{"code-review", "process-coaching"},
					nil),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "council", Name: "Council", Icon: "Vote",
			Description: "Specialist-reviewer panel for risky / multi-perspective decisions.",
			WhenToUse:   "Large architecture decisions; multi-perspective RFC review; security-sensitive changes.",
			SizeLabel:   "5", SortOrder: 101, Visible: false,
			Slots: []Slot{
				mk("impl-1", "full-stack-developer",
					[]string{"tdd", "debugging", "spec-driven", "api-design"},
					nil),
				mk("qa-1", "qa-engineer",
					[]string{"e2e-testing"},
					nil),
				mk("reviewer-1", "code-reviewer",
					[]string{"code-review"},
					nil),
				mk("sec-1", "security-engineer",
					[]string{"security-review"},
					nil),
				mk("arch-1", "architect",
					[]string{"planning", "api-design", "code-review"},
					nil),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "multi-team", Name: "Multi-Team", Icon: "Layers",
			Description: "Multi-team composition placeholder — combine multiple Squads / Scrum Teams under one workspace.",
			WhenToUse:   "Juggling multiple projects or sub-teams under one dashboard; operator composes member-by-member.",
			SizeLabel:   "0", SortOrder: 102, Visible: false,
			Slots:       []Slot{},
			ReadOnly:    true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
	}
}
