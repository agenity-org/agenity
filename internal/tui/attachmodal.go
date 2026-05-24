// Package tui — attachmodal.go is the pre-attach dismissible hint per the
// v0.3 spec. First-time `t` press shows a modal teaching the user how to
// detach back to chepherd (`Ctrl-B D`). Dismissal stored at
// ~/.config/chepherd/state.json so a returning user attaches instantly.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/chepherd/chepherd/internal/style"
)

// uiState is the persisted user-preference file at ~/.config/chepherd/state.json.
// Currently holds only the attach-hint dismissal; extensible later.
type uiState struct {
	HideAttachHint bool `json:"hide_attach_hint"`
}

func uiStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "chepherd", "state.json")
}

func loadUIState() uiState {
	var s uiState
	b, err := os.ReadFile(uiStatePath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func saveUIState(s uiState) error {
	p := uiStatePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// AttachModal is the pre-attach hint overlay. ShowOrAttach checks the
// persisted dismissal flag and either opens the modal (user must press
// Enter to attach, Esc to cancel, tab to toggle the dismissal checkbox)
// or attaches directly when the user has already dismissed it.
type AttachModal struct {
	app *App

	view     *tview.Flex
	dismiss  *tview.Checkbox
	tmuxName string

	// completion: called from the modal with true=attach, false=cancel
	onDecision func(attach bool, dismiss bool)
}

func newAttachModal(a *App) *AttachModal {
	m := &AttachModal{app: a}

	hint := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	hint.SetBackgroundColor(tcell.ColorBlack)
	hint.SetText(fmt.Sprintf("\n\n"+
		"  You're about to enter the live tmux session.\n\n"+
		"  To return to chepherd, press:\n\n"+
		"        %s   then   %s\n\n",
		style.TagBold(style.KeyLetter, "Ctrl+B"),
		style.TagBold(style.KeyLetter, "D")))

	m.dismiss = tview.NewCheckbox().
		SetLabel("don't show this again  ").
		SetChecked(false)
	m.dismiss.SetBackgroundColor(tcell.ColorBlack)
	m.dismiss.SetFieldBackgroundColor(tcell.ColorBlack)
	m.dismiss.SetLabelColor(style.Body)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(tcell.ColorBlack)
	footer.SetText(fmt.Sprintf("\n  %s attach   %s cancel\n",
		style.TagBold(style.KeyLetter, "Enter"),
		style.TagBold(style.KeyLetter, "Esc")))

	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(hint, 0, 1, false).
		AddItem(centerHoriz(m.dismiss, 30), 1, 0, true).
		AddItem(footer, 3, 0, false)
	box.SetBackgroundColor(tcell.ColorBlack)
	box.SetBorder(true).
		SetBorderColor(style.Border).
		SetTitleColor(style.Title).
		SetTitleAlign(tview.AlignCenter).
		SetBorderPadding(1, 1, 2, 2)

	// Bind Enter/Esc at the box level so the user doesn't have to focus
	// a specific button.
	box.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEnter:
			m.finish(true)
			return nil
		case tcell.KeyEsc:
			m.finish(false)
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			// Toggle the checkbox via tab — explicit affordance.
			m.dismiss.SetChecked(!m.dismiss.IsChecked())
			return nil
		case tcell.KeyRune:
			if ev.Rune() == ' ' {
				m.dismiss.SetChecked(!m.dismiss.IsChecked())
				return nil
			}
		}
		return ev
	})

	m.view = centerBox(box, 60, 14)
	return m
}

// ShowOrAttach checks the persisted dismissal flag. If hidden, runs
// onDecision(true, false) immediately. Otherwise opens the modal.
func (m *AttachModal) ShowOrAttach(tmuxName string, onDecision func(attach bool, dismiss bool)) {
	m.tmuxName = tmuxName
	m.onDecision = onDecision

	if loadUIState().HideAttachHint {
		onDecision(true, false)
		return
	}

	m.app.pages.AddPage("attach-modal", m.view, true, true)
	m.app.tv.SetFocus(m.view)
}

func (m *AttachModal) finish(attach bool) {
	dismiss := m.dismiss.IsChecked()
	m.app.pages.SwitchToPage("dashboard")
	m.app.pages.RemovePage("attach-modal")
	m.app.tv.SetFocus(m.app.dashboard.list)
	m.app.tv.Draw()

	if dismiss {
		_ = saveUIState(uiState{HideAttachHint: true})
	}
	if m.onDecision != nil {
		m.onDecision(attach, dismiss)
	}
}

// centerHoriz wraps an item in a flex that horizontally centers it
// within a fixed width.
func centerHoriz(item tview.Primitive, width int) *tview.Flex {
	f := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(nil, 0, 1, false).
		AddItem(item, width, 0, true).
		AddItem(nil, 0, 1, false)
	f.SetBackgroundColor(tcell.ColorBlack)
	return f
}

// centerBox horizontally and vertically centers an item inside the
// outer Flex with fixed dimensions.
func centerBox(item tview.Primitive, width, height int) *tview.Flex {
	row := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(nil, 0, 1, false).
		AddItem(item, width, 0, true).
		AddItem(nil, 0, 1, false)
	row.SetBackgroundColor(tcell.ColorBlack)
	out := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(row, height, 0, true).
		AddItem(nil, 0, 1, false)
	out.SetBackgroundColor(tcell.ColorBlack)
	return out
}
