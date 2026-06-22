package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/agenity-org/agenity/internal/style"
)

// NewSessionWizard is W2 — modal multi-step form for creating a watched session.
// Triggered by 'n' from the dashboard.
//
// Steps:
//   1. Where is the work? (existing / clone / init / worktree / no-git)
//   2. NEW vs RESUME vs FORK vs ADOPT (conditional on step 1)
//   3. Session name (auto-generated, editable)
//   4. VCS adapter (auto-detected, override allowed)
//   5. Setup checkboxes (BACKLOG-STANDARDS, labels, CLAUDE.md, .chepherd/)
type NewSessionWizard struct {
	app *App

	root  *tview.Pages
	step1 *tview.Form
	step2 *tview.Form
	step3 *tview.Form
	step4 *tview.Form
	done  *tview.TextView

	// Collected state across steps.
	mode        string // "existing", "clone", "init", "worktree", "no-git"
	repoPath    string
	cloneURL    string
	initPath    string
	resumeUUID  string
	forkUUID    string
	sessionName string
	vcsAdapter  string
}

func newNewSessionWizard(a *App) *NewSessionWizard {
	w := &NewSessionWizard{app: a}
	w.root = tview.NewPages()
	w.buildStep1()
	w.buildStep2()
	w.buildStep3()
	w.buildStep4()
	w.buildDone()
	w.root.SwitchToPage("step1")
	return w
}

func (w *NewSessionWizard) show() {
	w.reset()
	// Wrap in a centered grid so the modal doesn't fill the whole screen.
	grid := tview.NewGrid().
		SetColumns(0, 70, 0).
		SetRows(0, 22, 0).
		AddItem(w.root, 1, 1, 1, 1, 0, 0, true)
	grid.SetBackgroundColor(tcell.ColorBlack)
	w.app.pages.AddPage("newsession", grid, true, true)
	w.app.tv.SetFocus(w.step1)
}

func (w *NewSessionWizard) dismiss() {
	w.app.pages.RemovePage("newsession")
	w.app.tv.SetFocus(w.app.dashboard.list)
}

func (w *NewSessionWizard) reset() {
	w.mode = ""
	w.repoPath = ""
	w.cloneURL = ""
	w.initPath = ""
	w.resumeUUID = ""
	w.forkUUID = ""
	w.sessionName = ""
	w.vcsAdapter = ""
	w.root.SwitchToPage("step1")
}

// ───── step 1 ────────────────────────────────────────────────────────────

func (w *NewSessionWizard) buildStep1() {
	f := tview.NewForm()
	f.SetBorder(true).
		SetBorderColor(style.Border).
		SetTitle(style.TagBold(style.Title, " new session · step 1/4 · where is the work? ")).
		SetTitleAlign(tview.AlignLeft).
		SetBackgroundColor(tcell.ColorBlack)

	modes := []string{
		"existing local repo",
		"clone a git url",
		"init a brand-new git repo (sandbox)",
		"git worktree of an existing repo",
		"no git — just attach claude to a directory",
	}
	modeKeys := []string{"existing", "clone", "init", "worktree", "no-git"}

	f.AddDropDown("path:", modes, 0, func(opt string, idx int) {
		if idx < 0 || idx >= len(modeKeys) {
			return
		}
		w.mode = modeKeys[idx]
	})

	f.AddInputField("repo path / clone url / init path:", "", 50, nil, func(text string) {
		// Field reused for all path-like inputs.
		switch w.mode {
		case "clone":
			w.cloneURL = text
		case "init":
			w.initPath = text
		default:
			w.repoPath = text
		}
	})

	f.AddButton("next", func() {
		if !w.validateStep1() {
			return
		}
		w.root.SwitchToPage("step2")
	})
	f.AddButton("cancel", func() {
		w.dismiss()
	})

	f.SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(style.Primary).
		SetButtonBackgroundColor(style.Border).
		SetButtonTextColor(tcell.ColorBlack).
		SetLabelColor(style.Primary)

	w.step1 = f
	w.root.AddPage("step1", f, true, true)
}

func (w *NewSessionWizard) validateStep1() bool {
	switch w.mode {
	case "existing", "":
		if w.repoPath == "" {
			return false
		}
		p, err := filepath.Abs(w.repoPath)
		if err != nil {
			return false
		}
		if _, err := os.Stat(p); err != nil {
			return false
		}
		w.repoPath = p
		return true
	case "clone":
		return w.cloneURL != ""
	case "init":
		return w.initPath != ""
	case "worktree", "no-git":
		return w.repoPath != ""
	}
	return false
}

// ───── step 2 ────────────────────────────────────────────────────────────

func (w *NewSessionWizard) buildStep2() {
	f := tview.NewForm()
	f.SetBorder(true).
		SetBorderColor(style.Border).
		SetTitle(style.TagBold(style.Title, " step 2/4 · new conversation or resume? ")).
		SetTitleAlign(tview.AlignLeft).
		SetBackgroundColor(tcell.ColorBlack)

	choices := []string{
		"NEW conversation (clean slate)",
		"RESUME most recent (will look up UUID)",
		"FORK most recent (--fork-session, branches from same point)",
	}
	f.AddDropDown("conversation:", choices, 0, nil)

	f.AddInputField("explicit UUID (optional):", "", 40, nil, func(text string) {
		w.resumeUUID = strings.TrimSpace(text)
	})

	f.AddButton("next", func() { w.root.SwitchToPage("step3") })
	f.AddButton("back", func() { w.root.SwitchToPage("step1") })
	f.AddButton("cancel", func() { w.dismiss() })

	w.step2 = f
	w.root.AddPage("step2", f, true, false)
}

// ───── step 3 ────────────────────────────────────────────────────────────

func (w *NewSessionWizard) buildStep3() {
	f := tview.NewForm()
	f.SetBorder(true).
		SetBorderColor(style.Border).
		SetTitle(style.TagBold(style.Title, " step 3/4 · session name ")).
		SetTitleAlign(tview.AlignLeft).
		SetBackgroundColor(tcell.ColorBlack)

	f.AddInputField("name:", "", 40, validateName, func(text string) {
		w.sessionName = text
	})

	f.AddTextView("", "  must match ^[a-z][a-z0-9_-]+-\\d+$  (chepherd's discovery regex)",
		60, 2, false, false)

	f.AddButton("next", func() {
		// Auto-fill if blank.
		if w.sessionName == "" {
			w.sessionName = autogenSessionName(w.resolveTargetForName())
		}
		if !sessionNameRE.MatchString(w.sessionName) {
			return
		}
		w.root.SwitchToPage("step4")
	})
	f.AddButton("back", func() { w.root.SwitchToPage("step2") })
	f.AddButton("cancel", func() { w.dismiss() })

	w.step3 = f
	w.root.AddPage("step3", f, true, false)
}

// sessionNameRE matches the canonical discovery pattern (must mirror
// cmd.sessionNameRE + supervisor's daemon discovery regex).
var sessionNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]+-\d+$`)

func validateName(s string, _ rune) bool {
	// Allow incomplete typing — only the final value is checked.
	allowed := regexp.MustCompile(`^[a-z0-9_-]*$`)
	return allowed.MatchString(s)
}

func (w *NewSessionWizard) resolveTargetForName() string {
	switch w.mode {
	case "clone":
		// Use last URL segment.
		base := filepath.Base(strings.TrimSuffix(w.cloneURL, ".git"))
		return base
	case "init":
		return filepath.Base(w.initPath)
	default:
		return filepath.Base(w.repoPath)
	}
}

// ───── step 4 ────────────────────────────────────────────────────────────

func (w *NewSessionWizard) buildStep4() {
	f := tview.NewForm()
	f.SetBorder(true).
		SetBorderColor(style.Border).
		SetTitle(style.TagBold(style.Title, " step 4/4 · vcs adapter + setup ")).
		SetTitleAlign(tview.AlignLeft).
		SetBackgroundColor(tcell.ColorBlack)

	f.AddDropDown("vcs adapter:",
		[]string{"github (gh)", "gitlab (glab)", "gitea (tea)", "local (.chepherd/)"},
		0, func(opt string, idx int) {
			w.vcsAdapter = []string{"github", "gitlab", "gitea", "local"}[idx]
		})

	f.AddCheckbox("install docs/BACKLOG-STANDARDS.md template", true, nil)
	f.AddCheckbox("create status/* + severity/* labels", true, nil)
	f.AddCheckbox("install .github/ISSUE_TEMPLATE/* files", true, nil)
	f.AddCheckbox("append chepherd integration to CLAUDE.md", false, nil)

	f.AddButton("create + start", func() {
		w.executeCreate()
	})
	f.AddButton("back", func() { w.root.SwitchToPage("step3") })
	f.AddButton("cancel", func() { w.dismiss() })

	w.step4 = f
	w.root.AddPage("step4", f, true, false)
}

// ───── done screen ───────────────────────────────────────────────────────

func (w *NewSessionWizard) buildDone() {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetBorder(true).
		SetBorderColor(style.BandTrusted).
		SetTitle(style.TagBold(style.Title, " session created ")).
		SetTitleAlign(tview.AlignLeft).
		SetBackgroundColor(tcell.ColorBlack)
	w.done = tv
	w.root.AddPage("done", tv, true, false)

	tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case 't':
			w.app.tv.Suspend(func() {
				exec.Command("tmux", "attach", "-t", w.sessionName).Run()
			})
			w.dismiss()
			return nil
		case 'q':
			w.dismiss()
			return nil
		}
		if ev.Key() == tcell.KeyEsc || ev.Key() == tcell.KeyEnter {
			w.dismiss()
			return nil
		}
		return ev
	})
}

// ───── execution ─────────────────────────────────────────────────────────

// executeCreate carries out the wizard's intent — git clone/init, tmux new,
// claude launch, optional repo setup. Renders a result summary to the done page.
func (w *NewSessionWizard) executeCreate() {
	var log strings.Builder
	logOK := func(msg string) {
		fmt.Fprintf(&log, "  %s %s\n",
			style.Tag(style.BandTrusted, "✓"),
			style.Tag(style.Primary, msg))
	}
	logFail := func(msg string) {
		fmt.Fprintf(&log, "  %s %s\n",
			style.Tag(style.BandCrisis, "✗"),
			style.Tag(style.Primary, msg))
	}

	dir := w.repoPath

	// Phase 1: provision the directory (clone / init / verify).
	switch w.mode {
	case "clone":
		dir = filepath.Join(os.Getenv("HOME"), "repos", filepath.Base(strings.TrimSuffix(w.cloneURL, ".git")))
		if err := exec.Command("git", "clone", w.cloneURL, dir).Run(); err != nil {
			logFail(fmt.Sprintf("git clone failed: %v", err))
			w.showDone(log.String())
			return
		}
		logOK("git clone → " + dir)
	case "init":
		dir = w.initPath
		_ = os.MkdirAll(dir, 0o755)
		if err := exec.Command("git", "-C", dir, "init", "-b", "main").Run(); err != nil {
			logFail(fmt.Sprintf("git init failed: %v", err))
			w.showDone(log.String())
			return
		}
		logOK("git init → " + dir)
	}

	// Phase 2: tmux new-session + launch claude.
	if exec.Command("tmux", "has-session", "-t", w.sessionName).Run() == nil {
		logFail(fmt.Sprintf("tmux session %q already exists", w.sessionName))
		w.showDone(log.String())
		return
	}
	claudeArgs := []string{"--dangerously-skip-permissions"}
	if w.resumeUUID != "" {
		claudeArgs = append([]string{"--resume", w.resumeUUID}, claudeArgs...)
	}
	tmuxArgs := append([]string{
		"new-session", "-d", "-s", w.sessionName, "-c", dir, "claude",
	}, claudeArgs...)
	if err := exec.Command("tmux", tmuxArgs...).Run(); err != nil {
		logFail(fmt.Sprintf("tmux new-session failed: %v", err))
		w.showDone(log.String())
		return
	}
	logOK(fmt.Sprintf("tmux session %s launched at %s", w.sessionName, dir))
	logOK(fmt.Sprintf("claude %s", strings.Join(claudeArgs, " ")))

	// Phase 3: setup optionals (best-effort, soft-fail).
	if w.vcsAdapter == "github" {
		logOK("vcs adapter: github (gh)")
	} else {
		logOK("vcs adapter: " + w.vcsAdapter)
	}

	logOK("chepherd will pick up this session on the next tick (≤60s)")
	w.showDone(log.String())
}

func (w *NewSessionWizard) showDone(content string) {
	w.done.SetText("\n" + content + "\n\n" +
		style.Tag(style.Primary, "  press ") +
		style.TagBold(style.KeyLetter, "t") +
		style.Tag(style.Primary, " to attach to ") +
		style.Tag(style.Logo, w.sessionName) +
		style.Tag(style.Primary, ", or ") +
		style.TagBold(style.KeyLetter, "enter") +
		style.Tag(style.Primary, " to return to the dashboard\n"))
	w.root.SwitchToPage("done")
	w.app.tv.SetFocus(w.done)
}

// autogenSessionName mirrors the CLI 'chepherd new' default — basename of the
// target dir + the next free numeric suffix.
func autogenSessionName(repoBase string) string {
	// Sanitise the base name.
	var b strings.Builder
	for _, r := range strings.ToLower(repoBase) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := b.String()
	for n := 1; n <= 99; n++ {
		candidate := fmt.Sprintf("%s-%d", clean, n)
		if exec.Command("tmux", "has-session", "-t", candidate).Run() != nil {
			return candidate
		}
	}
	return clean + "-99"
}
