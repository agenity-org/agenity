package templateregistry

import "time"

// builtinSet returns the 6 visible Stage-1 templates + 3 hidden
// admin-only templates. Operator-confirmed per architect 2026-05-27.
//
// Visible (display order = sort_order ascending; 2×3 grid in Stage 1):
//   0. solo
//   1. pair
//   2. trio
//   3. scrum
//   4. review
//   5. custom
//
// Hidden (admin-only; not in Stage 1 grid):
//   100. solo-supervised
//   101. council
//   102. multi-team
//
// "Stack Trio" is BANNED — fabricated string never appears.
func builtinSet() []Template {
	now := time.Now().UTC()

	ccAnt := func(label, primary string) SkillSlot {
		return SkillSlot{
			Label:               label,
			PrimarySkill:        primary,
			AgentTypeDefault:    "claude-code",
			AccountClassDefault: "anthropic",
		}
	}

	return []Template{
		{
			ID: "solo", Name: "Solo", Icon: "User",
			Description: "One agent, one job. Exploration / quick spikes.",
			WhenToUse:   "Daily defaults; experimenting with a fresh repo.",
			SortOrder:   0, Visible: true,
			Slots: []SkillSlot{
				{
					Label:               "impl-1",
					PrimarySkill:        "implementer",
					AltSkills:           []string{"frontend-impl", "backend-impl", "docs-writer"},
					AgentTypeDefault:    "claude-code",
					AccountClassDefault: "anthropic",
				},
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "pair", Name: "Pair", Icon: "Users",
			Description: "XP pair-programming — one builds, one reviews.",
			WhenToUse:   "Two-set-of-eyes problems; mentoring; live review.",
			SortOrder:   1, Visible: true,
			Slots: []SkillSlot{
				ccAnt("impl-1", "implementer"),
				ccAnt("reviewer-1", "code-reviewer"),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "trio", Name: "Trio", Icon: "Network",
			Description: "Smallest balanced team: lead + builder + reviewer.",
			WhenToUse:   "Focused feature work that needs design + ship + check.",
			SortOrder:   2, Visible: true,
			Slots: []SkillSlot{
				ccAnt("lead-1", "tech-lead"),
				ccAnt("impl-1", "implementer"),
				ccAnt("reviewer-1", "code-reviewer"),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "scrum", Name: "Scrum Team", Icon: "KanbanSquare",
			Description: "Full scrum cadence for product sprints.",
			WhenToUse:   "Features needing upstream design review + downstream QA sign-off.",
			SortOrder:   3, Visible: true,
			Slots: []SkillSlot{
				ccAnt("po-1", "product-owner"),
				ccAnt("arch-1", "architect"),
				ccAnt("impl-1", "implementer"),
				ccAnt("qa-1", "qa-tester"),
				ccAnt("shepherd-1", "shepherd"),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "review", Name: "Review Squad", Icon: "Shield",
			Description: "High-stakes change with two reviewer lenses.",
			WhenToUse:   "Security-sensitive / auth / payments / supply-chain changes.",
			SortOrder:   4, Visible: true,
			Slots: []SkillSlot{
				ccAnt("impl-1", "implementer"),
				ccAnt("reviewer-1", "code-reviewer"),
				ccAnt("sec-1", "security-reviewer"),
				ccAnt("shepherd-1", "shepherd"),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "custom", Name: "Custom", Icon: "PlusCircle",
			Description: "Build your own team — pick skills, set count.",
			WhenToUse:   "You know exactly which roles you need.",
			SortOrder:   5, Visible: true,
			Slots:       []SkillSlot{}, // empty — operator composes in Stage 3
			ReadOnly:    true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},

		// Hidden / admin-only builtins (catalog legacy; operator can
		// flip Visible=true via /admin/templates).
		{
			ID: "solo-supervised", Name: "Solo (Supervised)", Icon: "UserCheck",
			Description: "Solo + shepherd watches.",
			WhenToUse:   "Single-driver work with continuous discipline scoring.",
			SortOrder:   100, Visible: false,
			Slots: []SkillSlot{
				ccAnt("impl-1", "implementer"),
				ccAnt("shepherd-1", "shepherd"),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "council", Name: "Council", Icon: "Vote",
			Description: "Specialist-reviewer panel for risky work.",
			WhenToUse:   "Large architecture decisions; multi-perspective RFC review.",
			SortOrder:   101, Visible: false,
			Slots: []SkillSlot{
				ccAnt("impl-1", "implementer"),
				ccAnt("qa-1", "qa-tester"),
				ccAnt("reviewer-1", "code-reviewer"),
				ccAnt("sec-1", "security-reviewer"),
				ccAnt("shepherd-1", "shepherd"),
			},
			ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "multi-team", Name: "Multi-Team", Icon: "Layers",
			Description: "Multi-team composition for cross-cutting work.",
			WhenToUse:   "Juggling multiple projects under one dashboard.",
			SortOrder:   102, Visible: false,
			Slots:       []SkillSlot{},
			ReadOnly:    true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
		},
	}
}
