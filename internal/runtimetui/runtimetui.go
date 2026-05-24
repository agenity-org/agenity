// Package runtimetui is chepherd v0.5's TUI client, wired against the
// new runtime.Runtime + messagebus.Relay + ptyhost.Session APIs.
//
// Separate package from internal/tui/ (the legacy tmux-based TUI) so the
// existing 'chepherd dashboard' command remains untouched while v0.5
// stabilizes.
//
// Layout:
//
//	┌─ chepherd · N sessions · tribe: default · 14:22 UTC ─────────────┐
//	├─ Sessions ──────┬─ Live: <selected> ────────────────────────────┤
//	│  ▶ adam         │  (live capture of selected session's output)  │
//	│    chepherd     │                                                │
//	│    iogrid-1     │                                                │
//	│                 │                                                │
//	├─────────────────┴────────────────────────────────────────────────┤
//	│  ↑↓ select  i interact  n spawn  p pause  q quit                 │
//	└──────────────────────────────────────────────────────────────────┘
package runtimetui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/runtime"
)

// App is the v0.5 TUI client.
type App struct {
	rt *runtime.Runtime
	tv *tview.Application

	header *tview.TextView
	list   *tview.Table
	center *tview.TextView
	footer *tview.TextView
	root   *tview.Flex

	mu             sync.Mutex
	selected       string                // session name currently focused
	centerSub      *session.Subscriber   // subscriber for selected session
	centerCancel   context.CancelFunc    // for tearing down old subscription
	interactMode   bool                  // routes keystrokes to selected session
	stoppingCenter chan struct{}
}

// New constructs the App. Call Run() to start.
func New(rt *runtime.Runtime) *App {
	a := &App{
		rt: rt,
		tv: tview.NewApplication(),
	}
	a.buildLayout()
	return a
}

func (a *App) buildLayout() {
	a.header = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	a.header.SetBackgroundColor(tcell.ColorBlack)

	a.list = tview.NewTable().SetBorders(false).SetSelectable(true, false).SetFixed(1, 0)
	a.list.SetBackgroundColor(tcell.ColorBlack)
	a.list.SetSelectedStyle(tcell.StyleDefault.
		Background(tcell.NewRGBColor(0x5F, 0x9E, 0xA0)).
		Foreground(tcell.ColorWhite).Bold(true))
	a.list.SetBorder(true).SetTitle(" Sessions ").SetTitleAlign(tview.AlignLeft)
	a.list.SetSelectionChangedFunc(func(row, _ int) {
		a.handleSelectionChange(row)
	})

	a.center = tview.NewTextView().SetDynamicColors(true).SetWrap(false).SetScrollable(true)
	a.center.SetBackgroundColor(tcell.ColorBlack)
	a.center.SetBorder(true).SetTitle(" (no selection) ").SetTitleAlign(tview.AlignLeft)

	a.footer = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	a.footer.SetBackgroundColor(tcell.ColorBlack)

	body := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(a.list, 0, 25, true).
		AddItem(a.center, 0, 75, false)

	a.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.header, 1, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(a.footer, 1, 0, false)
	a.root.SetBackgroundColor(tcell.ColorBlack)

	a.list.SetInputCapture(a.listInputCapture)
	a.center.SetInputCapture(a.centerInputCapture)
}

// listInputCapture handles hotkeys while the list has focus.
func (a *App) listInputCapture(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Rune() {
	case 'q', 'Q':
		a.Stop()
		return nil
	case 'i':
		a.enterInteractMode()
		return nil
	case 'n':
		a.showSpawnPrompt()
		return nil
	case 'p':
		a.togglePauseSelected()
		return nil
	case 'r':
		a.refreshList()
		return nil
	}
	if ev.Key() == tcell.KeyEnter {
		a.enterInteractMode()
		return nil
	}
	return ev
}

// centerInputCapture handles keystrokes while the center pane has focus
// (i.e. in interact mode — keystrokes route to the selected session's stdin).
func (a *App) centerInputCapture(ev *tcell.EventKey) *tcell.EventKey {
	a.mu.Lock()
	interacting := a.interactMode
	target := a.selected
	a.mu.Unlock()
	if !interacting {
		// Not in interact mode — defer to list.
		return ev
	}
	if ev.Key() == tcell.KeyEsc {
		a.exitInteractMode()
		return nil
	}
	if target == "" {
		return nil
	}
	s, _ := a.rt.Get(target)
	if s == nil {
		return nil
	}
	// Translate the EventKey into bytes for the PTY.
	bytes := keyToBytes(ev)
	if len(bytes) > 0 {
		_, _ = s.Write(bytes)
	}
	return nil
}

// keyToBytes converts a tcell EventKey into the bytes that would have
// been typed at a real terminal. Covers printable runes, common control
// keys, and arrow keys (CSI sequences).
func keyToBytes(ev *tcell.EventKey) []byte {
	switch ev.Key() {
	case tcell.KeyRune:
		return []byte(string(ev.Rune()))
	case tcell.KeyEnter:
		return []byte{'\r'}
	case tcell.KeyTab:
		return []byte{'\t'}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		return []byte{0x7f}
	case tcell.KeyDelete:
		return []byte{0x1b, '[', '3', '~'}
	case tcell.KeyUp:
		return []byte{0x1b, '[', 'A'}
	case tcell.KeyDown:
		return []byte{0x1b, '[', 'B'}
	case tcell.KeyRight:
		return []byte{0x1b, '[', 'C'}
	case tcell.KeyLeft:
		return []byte{0x1b, '[', 'D'}
	case tcell.KeyCtrlC:
		return []byte{0x03}
	case tcell.KeyCtrlD:
		return []byte{0x04}
	case tcell.KeyCtrlL:
		return []byte{0x0c}
	case tcell.KeyCtrlU:
		return []byte{0x15}
	case tcell.KeyCtrlW:
		return []byte{0x17}
	}
	return nil
}

// handleSelectionChange swaps the center pane to subscribe to the newly-selected session.
func (a *App) handleSelectionChange(row int) {
	infos := a.rt.List()
	idx := row - 1 // header row is 0
	if idx < 0 || idx >= len(infos) {
		return
	}
	name := infos[idx].Name
	a.selectSession(name)
}

func (a *App) selectSession(name string) {
	a.mu.Lock()
	if a.selected == name {
		a.mu.Unlock()
		return
	}
	if a.centerCancel != nil {
		a.centerCancel()
	}
	a.selected = name
	s, _ := a.rt.Get(name)
	if s == nil {
		a.center.SetText("")
		a.center.SetTitle(" (no session) ")
		a.mu.Unlock()
		return
	}
	a.center.SetTitle(fmt.Sprintf(" Live: %s ", name))
	a.center.Clear()
	sub, replay, err := s.Subscribe(256)
	if err != nil {
		a.center.SetText(fmt.Sprintf("[red]subscribe failed: %v", err))
		a.mu.Unlock()
		return
	}
	a.centerSub = sub
	ctx, cancel := context.WithCancel(context.Background())
	a.centerCancel = cancel
	a.mu.Unlock()

	if len(replay) > 0 {
		fmt.Fprintf(a.center, "%s", tview.Escape(string(replay)))
	}
	go a.consumeCenter(ctx, sub)
}

func (a *App) consumeCenter(ctx context.Context, sub *session.Subscriber) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done:
			return
		case chunk, ok := <-sub.Ch:
			if !ok {
				return
			}
			text := tview.Escape(string(chunk))
			a.tv.QueueUpdateDraw(func() {
				fmt.Fprintf(a.center, "%s", text)
				a.center.ScrollToEnd()
			})
		}
	}
}

func (a *App) enterInteractMode() {
	a.mu.Lock()
	target := a.selected
	if target == "" {
		a.mu.Unlock()
		return
	}
	a.interactMode = true
	a.mu.Unlock()
	a.tv.SetFocus(a.center)
	a.refreshFooter()
}

func (a *App) exitInteractMode() {
	a.mu.Lock()
	a.interactMode = false
	a.mu.Unlock()
	a.tv.SetFocus(a.list)
	a.refreshFooter()
}

func (a *App) togglePauseSelected() {
	a.mu.Lock()
	target := a.selected
	a.mu.Unlock()
	if target == "" {
		return
	}
	_, info := a.rt.Get(target)
	if info == nil {
		return
	}
	_ = a.rt.Pause(target, !info.Paused)
	a.refreshList()
}

// showSpawnPrompt opens a tiny modal to spawn a peer.
func (a *App) showSpawnPrompt() {
	input := tview.NewInputField().SetLabel(" Spawn agent (name, e.g. iogrid-1): ")
	input.SetBackgroundColor(tcell.ColorBlack)
	input.SetFieldBackgroundColor(tcell.ColorBlack)
	input.SetDoneFunc(func(key tcell.Key) {
		name := strings.TrimSpace(input.GetText())
		a.tv.SetRoot(a.root, true)
		a.tv.SetFocus(a.list)
		if key != tcell.KeyEnter || name == "" {
			return
		}
		_, _, err := a.rt.Spawn(runtime.SpawnSpec{
			Name:      name,
			AgentSlug: "claude-code",
			Tribe:     "default",
			Role:      runtime.RoleWorker,
		})
		if err != nil {
			a.center.SetText(fmt.Sprintf("[red]spawn failed: %v", err))
		}
		a.refreshList()
	})
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(input, 3, 0, true).
		AddItem(nil, 0, 1, false)
	a.tv.SetRoot(box, true)
	a.tv.SetFocus(input)
}

// refreshList re-renders the session list from runtime.List().
func (a *App) refreshList() {
	a.tv.QueueUpdateDraw(func() {
		prevRow, _ := a.list.GetSelection()
		a.list.Clear()
		a.list.SetCell(0, 0, tview.NewTableCell("[::b]Name[-:-:-]").SetSelectable(false))
		a.list.SetCell(0, 1, tview.NewTableCell("[::b]Role[-:-:-]").SetSelectable(false))
		a.list.SetCell(0, 2, tview.NewTableCell("[::b]Tribe[-:-:-]").SetSelectable(false))
		infos := a.rt.List()
		for i, info := range infos {
			row := i + 1
			marker := ""
			if info.Paused {
				marker = "[grey]⏸[-]"
			}
			a.list.SetCell(row, 0, tview.NewTableCell(fmt.Sprintf("%s %s", marker, info.Name)).SetReference(info.Name))
			a.list.SetCell(row, 1, tview.NewTableCell(string(info.Role)))
			a.list.SetCell(row, 2, tview.NewTableCell(info.Tribe))
		}
		// Restore selection
		target := prevRow
		if target < 1 {
			target = 1
		}
		if a.list.GetRowCount() > 1 && target < a.list.GetRowCount() {
			a.list.Select(target, 0)
		}
	})
}

// refreshHeader re-renders the header line.
func (a *App) refreshHeader() {
	a.tv.QueueUpdateDraw(func() {
		infos := a.rt.List()
		tribes := map[string]bool{}
		for _, info := range infos {
			tribes[info.Tribe] = true
		}
		now := time.Now().UTC().Format("15:04:05 UTC")
		a.header.SetText(fmt.Sprintf("[orange]chepherd[-]  ·  %d sessions  ·  %d tribes  ·  %s",
			len(infos), len(tribes), now))
	})
}

// refreshFooter re-renders the footer hotkey strip.
func (a *App) refreshFooter() {
	a.tv.QueueUpdateDraw(func() {
		mode := "select"
		extra := ""
		a.mu.Lock()
		if a.interactMode {
			mode = "[red]interact[-]"
			extra = "   [yellow]Esc to exit interact[-]"
		}
		a.mu.Unlock()
		a.footer.SetText(fmt.Sprintf("  [::b]↑↓[-:-:-] select   [::b]Enter/i[-:-:-] interact   [::b]n[-:-:-] spawn   [::b]p[-:-:-] pause   [::b]r[-:-:-] refresh   [::b]q[-:-:-] quit   ·   mode: %s%s",
			mode, extra))
	})
}

// Run blocks until the user quits.
func (a *App) Run() error {
	a.refreshList()
	a.refreshHeader()
	a.refreshFooter()
	// Periodic refresh ticker
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		for range t.C {
			a.refreshHeader()
			a.refreshList()
		}
	}()
	// Auto-select first row if any
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.tv.QueueUpdateDraw(func() {
			if a.list.GetRowCount() > 1 {
				a.list.Select(1, 0)
			}
		})
	}()
	return a.tv.SetRoot(a.root, true).EnableMouse(true).Run()
}

// Stop terminates the TUI cleanly.
func (a *App) Stop() {
	a.tv.Stop()
}
