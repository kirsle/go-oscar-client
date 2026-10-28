package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// menuItem is an entry of the menu. An item without run is a heading, or a
// separator if it has no label either.
type menuItem struct {
	label string
	// key is the shortcut shown beside the label, if any.
	key string
	// link is the short label of the item where it's also offered under the
	// window tabs, or empty if it's only in the menu.
	link string
	run  func() tea.Cmd
}

// action is a clickable part of a line, such as a key of the key help.
type action struct {
	text string
	// run is called when the action is clicked. Actions without run are
	// shown but not clickable.
	run func() tea.Cmd
}

// inputPrompt asks for a value in the input line, such as the screen name of
// a buddy to add.
type inputPrompt struct {
	label string
	run   func(value string) tea.Cmd
	// draft is the text that was in the input line, put back when the prompt
	// ends.
	draft string
}

// ask prompts for a value in the input line, starting with initial, and
// calls run with it once it's entered.
func (s *sessionModel) ask(label, initial string, run func(value string) tea.Cmd) {
	draft := s.input.Value()
	if s.prompt != nil {
		draft = s.prompt.draft
	}
	s.prompt = &inputPrompt{label: label, run: run, draft: draft}
	s.input.SetValue(initial)
	s.input.CursorEnd()
	s.focusInput()
}

// endPrompt ends the prompt, putting back the text that was in the input
// line, and returns the value entered.
func (s *sessionModel) endPrompt() string {
	value := strings.TrimSpace(s.input.Value())
	s.input.SetValue(s.prompt.draft)
	s.input.CursorEnd()
	s.prompt = nil
	return value
}

// submitPrompt ends the prompt and acts on the value entered, if any.
func (s *sessionModel) submitPrompt() tea.Cmd {
	p := s.prompt
	if value := s.endPrompt(); value != "" {
		return p.run(value)
	}
	return nil
}

// cancelPrompt ends the prompt without acting on it.
func (s *sessionModel) cancelPrompt() tea.Cmd {
	s.endPrompt()
	return nil
}

// command returns a function that runs a slash command, for menu items that
// do what a command does.
func (s *sessionModel) command(line string) func() tea.Cmd {
	return func() tea.Cmd { return s.runCommand(line) }
}

func (s *sessionModel) askIM() tea.Cmd {
	s.ask("IM to", "", func(sn string) tea.Cmd {
		s.activate(s.imWindow(sn))
		return nil
	})
	return nil
}

func (s *sessionModel) askAddBuddy() tea.Cmd {
	s.ask("Add buddy", "", func(sn string) tea.Cmd { return s.addBuddy("", sn) })
	return nil
}

func (s *sessionModel) askInfo() tea.Cmd {
	s.ask("Info for", "", s.getInfo)
	return nil
}

func (s *sessionModel) askJoin() tea.Cmd {
	s.ask("Join room", "", s.joinChat)
	return nil
}

func (s *sessionModel) askBlock() tea.Cmd {
	s.ask("Block", "", s.block)
	return nil
}

func (s *sessionModel) askInvite() tea.Cmd {
	s.ask("Invite (name [message])", "", func(v string) tea.Cmd { return s.runCommand("/invite " + v) })
	return nil
}

func (s *sessionModel) askAway() tea.Cmd {
	msg := defaultAwayMessage
	if away := s.client.AwayMessage(); away != "" {
		msg = away
	} else if a := s.account(); a != nil && a.AwayMessage != "" {
		msg = a.AwayMessage
	}
	s.ask("Away message", msg, func(v string) tea.Cmd { return s.runCommand("/away " + v) })
	return nil
}

// windowActions are the actions on the active window's correspondent or chat
// room, offered in the menu and, if they have a link, under the window tabs.
func (s *sessionModel) windowActions(w *window) []menuItem {
	switch {
	case w.kind == winChat && w.room != nil:
		return []menuItem{
			{label: "Invite to room…", link: "Invite", run: s.askInvite},
			{label: "List users", run: s.command("/users")},
			{label: "Leave room", link: "Leave", run: s.command("/leave")},
		}
	case w.kind == winChat:
		name := strings.TrimPrefix(w.title, "#")
		return []menuItem{{label: "Rejoin room", link: "Rejoin", run: func() tea.Cmd { return s.joinChat(name) }}}
	case (w.kind == winIM || w.kind == winInfo) && w.peer != "":
		sn := w.peer
		var items []menuItem
		if w.kind == winInfo {
			items = append(items, menuItem{label: "Send IM", link: "IM", run: func() tea.Cmd {
				s.activate(s.imWindow(sn))
				s.focusInput()
				return nil
			}})
		} else {
			items = append(items, menuItem{label: "View info", link: "Info", run: func() tea.Cmd { return s.getInfo(sn) }})
		}
		if s.client.IsBuddy(sn) {
			items = append(items, menuItem{label: "Remove from buddy list", run: func() tea.Cmd { return s.removeBuddy(sn) }})
		} else {
			items = append(items, menuItem{label: "Add to buddy list", link: "Add buddy", run: func() tea.Cmd { return s.addBuddy("", sn) }})
		}
		if s.client.IsBlocked(sn) {
			items = append(items, menuItem{label: "Unblock", link: "Unblock", run: func() tea.Cmd { return s.unblock(sn) }})
		} else {
			items = append(items, menuItem{label: "Block", link: "Block", run: func() tea.Cmd { return s.block(sn) }})
		}
		// warning has no link, so it isn't a stray click away
		return append(items,
			menuItem{label: "Warn", run: func() tea.Cmd { return s.warn(sn, false) }},
			menuItem{label: "Warn anonymously", run: func() tea.Cmd { return s.warn(sn, true) }},
		)
	}
	return nil
}

// menuItems are the entries of the menu, which does what the slash commands
// do.
func (s *sessionModel) menuItems() []menuItem {
	items := []menuItem{
		{label: "Send IM…", run: s.askIM},
		{label: "Add buddy…", run: s.askAddBuddy},
		{label: "View info…", run: s.askInfo},
		{label: "Join chat room…", run: s.askJoin},
	}
	if inv := s.lastInvite; inv != nil {
		items = append(items, menuItem{label: "Accept invite to " + inv.RoomName, run: s.command("/accept")})
	}

	w := s.current()
	if acts := s.windowActions(w); len(acts) > 0 || s.active != 0 {
		items = append(items, menuItem{}, menuItem{label: w.title})
		items = append(items, acts...)
		if s.active != 0 {
			items = append(items, menuItem{label: "Close window", key: "^W", run: s.command("/close")})
		}
	}

	items = append(items, menuItem{}, menuItem{label: "Edit profile…", run: s.command("/profile")})
	if s.client.AwayMessage() == "" {
		items = append(items, menuItem{label: "Set away…", run: s.askAway})
	} else {
		items = append(items,
			menuItem{label: "I'm back", run: s.command("/back")},
			menuItem{label: "Change away message…", run: s.askAway},
		)
	}
	return append(items,
		menuItem{label: "Message style", run: s.command("/font")},
		menuItem{label: "Block user…", run: s.askBlock},
		menuItem{label: "Blocked users", run: s.command("/blocks")},
		menuItem{label: "Help", run: s.command("/help")},
		menuItem{},
		menuItem{label: "Sign off", run: s.command("/signoff")},
		menuItem{label: "Quit", key: "^C", run: s.command("/quit")},
	)
}

func (s *sessionModel) openMenu() {
	s.menuOpen = true
	s.menuCursor = -1
	s.moveMenuCursor(s.menuItems(), 1)
}

func (s *sessionModel) closeMenu() tea.Cmd {
	s.menuOpen = false
	return nil
}

func (s *sessionModel) toggleMenu() tea.Cmd {
	if s.menuOpen {
		return s.closeMenu()
	}
	s.openMenu()
	return nil
}

// moveMenuCursor moves the menu cursor to the next item in direction dir,
// skipping headings and separators.
func (s *sessionModel) moveMenuCursor(items []menuItem, dir int) {
	for i := s.menuCursor + dir; i >= 0 && i < len(items); i += dir {
		if items[i].run != nil {
			s.menuCursor = i
			return
		}
	}
}

// clampMenuCursor keeps the menu cursor on an item, as the items change with
// the active window.
func (s *sessionModel) clampMenuCursor(items []menuItem) {
	s.menuCursor = min(max(s.menuCursor, 0), len(items)-1)
	if items[s.menuCursor].run == nil {
		s.moveMenuCursor(items, 1)
	}
	if items[s.menuCursor].run == nil {
		s.moveMenuCursor(items, -1)
	}
}

// chooseMenuItem closes the menu and runs its item.
func (s *sessionModel) chooseMenuItem(item menuItem) tea.Cmd {
	s.closeMenu()
	return item.run()
}

func (s *sessionModel) handleMenuKey(msg tea.KeyMsg) tea.Cmd {
	items := s.menuItems()
	s.clampMenuCursor(items)
	switch msg.String() {
	case "up", "k", "shift+tab":
		s.moveMenuCursor(items, -1)
	case "down", "j", "tab":
		s.moveMenuCursor(items, 1)
	case "enter", " ":
		return s.chooseMenuItem(items[s.menuCursor])
	case "esc", "f10", "alt+m":
		return s.closeMenu()
	}
	return nil
}

// menuView renders the open menu at screen position x, y in up to width by
// height cells, scrolled to the cursor, and makes its items clickable.
func (s *sessionModel) menuView(x, y, width, height int) string {
	s.menuZones = s.menuZones[:0]
	items := s.menuItems()
	s.clampMenuCursor(items)

	border := 0
	if width >= 12 && height >= 3 {
		border = 1
	}
	inner := 0
	for _, it := range items {
		w := lipgloss.Width(it.label)
		if it.key != "" {
			w += 2 + lipgloss.Width(it.key)
		}
		inner = max(inner, w+2)
	}
	inner = min(inner, width-2*border)
	visible := height - 2*border
	if inner <= 0 || visible <= 0 {
		return ""
	}

	start := min(max(0, s.menuCursor-visible+1), max(0, len(items)-visible))
	var rows []string
	for i := start; i < len(items) && len(rows) < visible; i++ {
		it := items[i]
		var row string
		switch {
		case it.label == "":
			row = dimStyle.Render(strings.Repeat("─", inner))
		case it.run == nil:
			row = dimStyle.Render(truncate(" "+it.label, inner))
		default:
			row = " " + it.label
			if it.key != "" {
				key := dimStyle.Render(it.key) + " "
				if gap := inner - lipgloss.Width(row) - lipgloss.Width(key); gap >= 2 {
					row += strings.Repeat(" ", gap) + key
				}
			}
			row = truncate(row, inner)
			row += strings.Repeat(" ", inner-lipgloss.Width(row))
			if i == s.menuCursor {
				row = selectedStyle.Render(row)
			}
			s.menuZones = append(s.menuZones, zone{
				x: x + border, y: y + border + len(rows), width: inner,
				click: func() tea.Cmd { return s.chooseMenuItem(it) },
			})
		}
		rows = append(rows, row)
	}

	style := lipgloss.NewStyle().Width(inner)
	if border > 0 {
		style = style.Border(lipgloss.RoundedBorder()).BorderForeground(accentColor)
	}
	return style.Render(strings.Join(rows, "\n"))
}

// actionBar renders the first of bars, from longest to shortest, that fits in
// width, or the last one cut off, at screen position x, y. The actions shown
// in full are clickable.
func (s *sessionModel) actionBar(x, y, width int, sep string, bars ...[]action) string {
	if len(bars) == 0 {
		return ""
	}
	bar := bars[len(bars)-1]
	for _, b := range bars {
		if barWidth(b, sep) <= width {
			bar = b
			break
		}
	}
	var b strings.Builder
	col := 0
	for i, a := range bar {
		if i > 0 {
			b.WriteString(sep)
			col += lipgloss.Width(sep)
		}
		w := lipgloss.Width(a.text)
		if a.run != nil && col+w <= width {
			s.addZone(x+col, y, w, a.run)
		}
		b.WriteString(a.text)
		col += w
	}
	return truncate(b.String(), width)
}

// barWidth is the width of a bar of actions separated by sep.
func barWidth(bar []action, sep string) int {
	w := 0
	for i, a := range bar {
		if i > 0 {
			w += lipgloss.Width(sep)
		}
		w += lipgloss.Width(a.text)
	}
	return w
}

// helpKey is an entry of the key help: a key and what it does, which can be
// clicked as well as pressed if run is set.
func helpKey(key, label string, run func() tea.Cmd) action {
	text := keyStyle.Render(key)
	if label != "" {
		text += dimStyle.Render(": " + label)
	}
	return action{text: text, run: run}
}

// helpBars are the key help line, from longest to shortest.
func (s *sessionModel) helpBars() [][]action {
	tab := func() tea.Cmd { return s.toggleFocus() }
	switch {
	case s.menuOpen:
		esc := helpKey("Esc", "close", s.closeMenu)
		return [][]action{
			{helpKey("↑/↓", "select", nil), helpKey("Enter", "choose", nil), esc},
			{esc},
		}
	case s.prompt != nil:
		return [][]action{{helpKey("Enter", "OK", s.submitPrompt), helpKey("Esc", "cancel", s.cancelPrompt)}}
	case s.focus == focusBuddies:
		return s.buddyHelpBars()
	}

	menu := helpKey("F10", "menu", s.toggleMenu)
	next := func() tea.Cmd { return s.runCommand("/next") }
	closeWin := s.command("/close")
	help := helpKey("/help", "", s.command("/help"))
	return [][]action{
		{menu, helpKey("Tab", "buddy list", tab), helpKey("Ctrl+N/P", "switch window", next), helpKey("Ctrl+W", "close", closeWin),
			helpKey("PgUp/PgDn", "scroll", nil), help, helpKey("Ctrl+C", "quit", s.command("/quit"))},
		{menu, helpKey("Tab", "buddies", tab), helpKey("^N/^P", "windows", next), helpKey("^W", "close", closeWin), help},
		{menu, helpKey("Tab", "buddies", tab), help},
		{menu},
	}
}

// buddyHelpBars are the key help line while the buddy list has focus.
func (s *sessionModel) buddyHelpBars() [][]action {
	tab := func() tea.Cmd { return s.toggleFocus() }
	selectKey := helpKey("↑/↓", "select", nil)
	if row, ok := s.selectedRow(); ok && row.action != nil {
		return [][]action{
			{selectKey, helpKey("Enter", row.label, row.action), helpKey("Tab", "input", tab)},
			{helpKey("Enter", row.label, row.action), helpKey("Tab", "input", tab)},
			{helpKey("Tab", "input", tab)},
		}
	}
	add := s.buddyKey("a")
	row, ok := s.selectedBuddy()
	if !ok {
		return [][]action{
			{helpKey("a", "add buddy", add), helpKey("Tab", "input", tab)},
			{helpKey("Tab", "input", tab)},
		}
	}
	block, remove := "block", "remove"
	if row.blocked || s.client.IsBlocked(row.screenName) {
		block = "unblock"
	}
	if row.blocked {
		remove = "unblock"
	}
	enter, info, warn, b, d := s.buddyKey("enter"), s.buddyKey("i"), s.buddyKey("w"), s.buddyKey("b"), s.buddyKey("d")
	return [][]action{
		{selectKey, helpKey("Enter", "IM", enter), helpKey("i", "info", info), helpKey("w", "warn", warn),
			helpKey("W", "warn anonymously", s.buddyKey("W")), helpKey("b", block, b), helpKey("d", remove, d),
			helpKey("a", "add", add), helpKey("Tab", "input", tab)},
		{helpKey("Enter", "IM", enter), helpKey("i", "info", info), helpKey("w", "warn", warn), helpKey("b", block, b),
			helpKey("d", remove, d), helpKey("a", "add", add), helpKey("Tab", "input", tab)},
		{helpKey("Enter", "", enter), helpKey("i", "", info), helpKey("w", "", warn), helpKey("b", "", b),
			helpKey("d", "", d), helpKey("a", "", add), helpKey("Tab", "", tab)},
		{helpKey("Tab", "input", tab)},
	}
}

// buddyKey returns a function that acts on the selected buddy as if key were
// pressed in the buddy list.
func (s *sessionModel) buddyKey(key string) func() tea.Cmd {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if key == "enter" {
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	}
	return func() tea.Cmd { return s.handleBuddyKey(msg) }
}
