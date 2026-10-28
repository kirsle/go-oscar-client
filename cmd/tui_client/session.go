package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kirsle/go-oscar-client/client"
	"github.com/kirsle/go-oscar-client/client/htmlterm"
	"github.com/mk6i/open-oscar-server/wire"
)

// requestTimeout bounds how long a request to the server may take.
const requestTimeout = 15 * time.Second

// maxWindowLines is how many lines a window keeps.
const maxWindowLines = 1000

// wheelLines is how many lines the mouse wheel scrolls a window.
const wheelLines = 3

// defaultAwayMessage is used by /away when no message is given or saved.
const defaultAwayMessage = "I am away from my computer right now."

type winKind int

const (
	winStatus winKind = iota
	winIM
	winChat
	winInfo
)

// window is a tab in the main pane: the status log, an IM conversation, a
// chat room or a user's info.
type window struct {
	kind  winKind
	key   string
	title string
	// peer is the IM correspondent or the user whose info is shown.
	peer   string
	room   *client.ChatRoom
	lines  []logLine
	unread bool
	// scroll is how many lines the view is scrolled up from the bottom.
	scroll int
	// gotUsers is set once a chat room window has listed the room's users.
	gotUsers bool
}

// logLine is a line of a window. Its timestamp is shown if the terminal is
// wide enough.
type logLine struct {
	// at is when the line was logged, or zero for lines without a timestamp.
	at   time.Time
	text string
}

// add adds a line without a timestamp.
func (w *window) add(text string) {
	w.addAt(time.Time{}, text)
}

// addAt adds a line with a timestamp.
func (w *window) addAt(at time.Time, text string) {
	w.lines = append(w.lines, logLine{at: at, text: text})
	if len(w.lines) > maxWindowLines {
		w.lines = w.lines[len(w.lines)-maxWindowLines:]
	}
}

// buddyRow is a line of the buddy list pane.
type buddyRow struct {
	text string
	// screenName is set on buddy rows and empty on group headers.
	screenName string
	// blocked marks rows of the block list.
	blocked bool
	// wrap marks rows that are wrapped to the pane width instead of cut off.
	// Only the hint shown in place of an empty buddy list is, since the
	// buddy list scrolls by rows.
	wrap bool
}

// Session pane focus.
const (
	focusInput = iota
	focusBuddies
)

// clientEventMsg carries an event from the client.
type clientEventMsg struct {
	client *client.Client
	ev     client.Event
}

// noticeMsg is a line to show in a window, or the status window if key is
// empty, usually the result of a request.
type noticeMsg struct {
	key  string
	text string
	err  error
}

// infoMsg carries the result of a user info lookup.
type infoMsg struct {
	screenName string
	profile    client.UserProfile
	err        error
}

// sessionModel is the screen shown while signed on.
type sessionModel struct {
	cfg        *config
	client     *client.Client
	server     string
	screenName string
	plain      bool

	ctx    context.Context
	cancel context.CancelFunc

	windows []*window
	active  int

	buddyRows   []buddyRow
	buddyCursor int
	online      map[string]client.UserInfo
	focus       int

	input   textinput.Model
	editor  textarea.Model
	editing bool

	// zones are the clickable parts of the screen, found as it's drawn.
	zones []zone

	lastInvite  *client.ChatInvite
	awayReplied map[string]bool
	rateLimited bool
}

func newSessionModel(cfg *config, msg signedOnMsg, plain bool) *sessionModel {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sessionModel{
		cfg:         cfg,
		client:      msg.client,
		server:      msg.server,
		screenName:  msg.client.Self().ScreenName,
		plain:       plain,
		ctx:         ctx,
		cancel:      cancel,
		online:      make(map[string]client.UserInfo),
		awayReplied: make(map[string]bool),
	}
	if s.screenName == "" {
		s.screenName = msg.screenName
	}

	s.input = textinput.New()
	s.input.Prompt = ""
	s.input.CharLimit = 2000
	s.input.Focus()

	s.editor = textarea.New()
	s.editor.Placeholder = "Write your profile. HTML such as <b>bold</b> and <font color=\"#FF0000\">red</font> is supported."
	s.editor.CharLimit = 4000
	s.editor.ShowLineNumbers = false

	s.windows = []*window{{kind: winStatus, key: "status", title: "Status"}}
	s.status(fmt.Sprintf("Signed on to %s as %s. Type /help for commands.", s.server, s.screenName))
	s.refreshBuddies()
	return s
}

func (s *sessionModel) init() tea.Cmd {
	return tea.Batch(textinput.Blink, s.waitEvent())
}

// waitEvent waits for the next client event.
func (s *sessionModel) waitEvent() tea.Cmd {
	c, ctx := s.client, s.ctx
	return func() tea.Msg {
		select {
		case ev := <-c.Events():
			return clientEventMsg{client: c, ev: ev}
		case <-ctx.Done():
			return nil
		}
	}
}

// close signs off.
func (s *sessionModel) close() {
	s.cancel()
	s.client.Close()
}

// account returns the saved account of the signed-on user.
func (s *sessionModel) account() *account {
	return s.cfg.find(s.server, s.screenName)
}

// render converts HTML from the server for display.
func (s *sessionModel) render(text string) string {
	if s.plain {
		return htmlterm.Strip(text)
	}
	return htmlterm.ToANSI(text, lipgloss.HasDarkBackground())
}

func timestamp(t time.Time) string {
	return dimStyle.Render(t.Format("15:04"))
}

// window lookup and management

func (s *sessionModel) find(key string) *window {
	for _, w := range s.windows {
		if w.key == key {
			return w
		}
	}
	return nil
}

func (s *sessionModel) open(w *window) *window {
	if existing := s.find(w.key); existing != nil {
		return existing
	}
	s.windows = append(s.windows, w)
	return w
}

func (s *sessionModel) activate(w *window) {
	for i, win := range s.windows {
		if win == w {
			s.active = i
			w.unread = false
			return
		}
	}
}

func (s *sessionModel) current() *window {
	return s.windows[s.active]
}

func imKey(screenName string) string {
	return "im:" + client.NormalizeScreenName(screenName)
}

func (s *sessionModel) imWindow(screenName string) *window {
	return s.open(&window{kind: winIM, key: imKey(screenName), title: screenName, peer: screenName})
}

func (s *sessionModel) closeWindow(i int) {
	if i == 0 {
		return // the status window stays
	}
	w := s.windows[i]
	if w.kind == winChat && w.room != nil {
		w.room.Leave()
	}
	s.windows = append(s.windows[:i], s.windows[i+1:]...)
	if s.active >= len(s.windows) {
		s.active = len(s.windows) - 1
	}
}

// appendTo adds a line to w with a timestamp, or none if at is zero, marking
// w unread if it's in the background.
func (s *sessionModel) appendTo(w *window, at time.Time, text string) {
	w.addAt(at, text)
	if w != s.current() {
		w.unread = true
	}
}

// status logs a line to the status window.
func (s *sessionModel) status(text string) {
	s.appendTo(s.windows[0], time.Now(), statusStyle.Render(text))
}

// notify logs an important line to the status window and to the active
// window.
func (s *sessionModel) notify(text string, isErr bool) {
	style := statusStyle
	if isErr {
		style = errorStyle
	}
	now, line := time.Now(), style.Render(text)
	s.appendTo(s.windows[0], now, line)
	if s.active != 0 {
		s.current().addAt(now, line)
	}
}

// buddy list

func (s *sessionModel) refreshBuddies() {
	var rows []buddyRow
	for _, g := range s.client.BuddyList() {
		on := 0
		for _, b := range g.Buddies {
			if b.Online {
				on++
			}
		}
		rows = append(rows, buddyRow{text: groupStyle.Render(fmt.Sprintf("%s (%d/%d)", g.Name, on, len(g.Buddies)))})
		// online buddies first, then offline ones
		for _, online := range []bool{true, false} {
			for _, b := range g.Buddies {
				if b.Online == online {
					rows = append(rows, buddyRow{text: s.buddyText(b), screenName: b.ScreenName})
				}
			}
		}
	}
	if blocked := s.client.BlockList(); len(blocked) > 0 {
		rows = append(rows, buddyRow{text: groupStyle.Render(fmt.Sprintf("Blocked (%d)", len(blocked)))})
		for _, sn := range blocked {
			rows = append(rows, buddyRow{text: errorStyle.Render("⊘ ") + sn, screenName: sn, blocked: true})
		}
	}
	if len(rows) == 0 {
		rows = append(rows, buddyRow{text: dimStyle.Render("No buddies. Use /add <name>"), wrap: true})
	}
	s.buddyRows = rows
	s.clampBuddyCursor(1)
}

func (s *sessionModel) buddyText(b client.Buddy) string {
	name := b.ScreenName
	if b.Online && b.Info.ScreenName != "" {
		name = b.Info.ScreenName // formatted screen name
	}
	switch {
	case !b.Online:
		return dimStyle.Render("○ " + name)
	case b.Info.Away:
		return awayStyle.Render("◐ ") + name + dimStyle.Render(" away")
	case b.Info.IdleMinutes > 0:
		return awayStyle.Render("● ") + name + dimStyle.Render(fmt.Sprintf(" idle %s", idleText(b.Info.IdleMinutes)))
	default:
		text := onlineStyle.Render("● ") + name
		if b.Info.WarningLevel > 0 {
			text += dimStyle.Render(fmt.Sprintf(" %d%%", b.Info.WarningLevel))
		}
		return text
	}
}

func idleText(minutes int) string {
	if minutes >= 60 {
		return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// clampBuddyCursor keeps the cursor on a buddy row, moving in direction dir
// if it's on a header.
func (s *sessionModel) clampBuddyCursor(dir int) {
	if s.buddyCursor >= len(s.buddyRows) {
		s.buddyCursor = len(s.buddyRows) - 1
	}
	if s.buddyCursor < 0 {
		s.buddyCursor = 0
	}
	for i := s.buddyCursor; i >= 0 && i < len(s.buddyRows); i += dir {
		if s.buddyRows[i].screenName != "" {
			s.buddyCursor = i
			return
		}
	}
	for i := s.buddyCursor; i >= 0 && i < len(s.buddyRows); i -= dir {
		if s.buddyRows[i].screenName != "" {
			s.buddyCursor = i
			return
		}
	}
}

func (s *sessionModel) moveBuddyCursor(dir int) {
	for i := s.buddyCursor + dir; i >= 0 && i < len(s.buddyRows); i += dir {
		if s.buddyRows[i].screenName != "" {
			s.buddyCursor = i
			return
		}
	}
}

func (s *sessionModel) selectedBuddy() (buddyRow, bool) {
	if s.buddyCursor < 0 || s.buddyCursor >= len(s.buddyRows) {
		return buddyRow{}, false
	}
	row := s.buddyRows[s.buddyCursor]
	return row, row.screenName != ""
}

// update

func (s *sessionModel) update(msg tea.Msg, width, height int) tea.Cmd {
	switch msg := msg.(type) {
	case clientEventMsg:
		if msg.client != s.client {
			return nil // from a previous session
		}
		return s.handleEvent(msg.ev)
	case noticeMsg:
		text := msg.text
		if msg.err != nil {
			text = msg.err.Error()
			if msg.text != "" {
				text = msg.text + ": " + msg.err.Error()
			}
		}
		if w := s.find(msg.key); msg.key != "" && w != nil {
			style := statusStyle
			if msg.err != nil {
				style = errorStyle
			}
			s.appendTo(w, time.Now(), style.Render(text))
		} else {
			s.notify(text, msg.err != nil)
		}
		return nil
	case infoMsg:
		if msg.err != nil {
			var snacErr *client.SNACError
			if errors.As(msg.err, &snacErr) && snacErr.Code == wire.ErrorCodeNotLoggedOn {
				s.notify(fmt.Sprintf("%s is offline or blocked.", msg.screenName), true)
			} else {
				s.notify(fmt.Sprintf("Unable to get info for %s: %v", msg.screenName, msg.err), true)
			}
			return nil
		}
		s.showInfo(msg.screenName, msg.profile)
		return nil
	case tea.KeyMsg:
		if s.editing {
			return s.updateEditor(msg)
		}
		return s.handleKey(msg, height)
	case tea.MouseMsg:
		if !s.editing {
			s.handleMouse(msg)
		}
		return nil
	}

	if s.editing {
		var cmd tea.Cmd
		s.editor, cmd = s.editor.Update(msg)
		return cmd
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return cmd
}

func (s *sessionModel) handleKey(msg tea.KeyMsg, height int) tea.Cmd {
	page := max(1, height/2)
	switch msg.String() {
	case "tab":
		if s.focus == focusInput {
			s.focus = focusBuddies
			s.input.Blur()
		} else {
			s.focusInput()
		}
		return nil
	case "ctrl+n", "alt+right":
		s.activate(s.windows[(s.active+1)%len(s.windows)])
		return nil
	case "ctrl+p", "alt+left":
		s.activate(s.windows[(s.active-1+len(s.windows))%len(s.windows)])
		return nil
	case "ctrl+w":
		s.closeWindow(s.active)
		return nil
	case "pgup":
		s.current().scroll += page
		return nil
	case "pgdown":
		s.current().scroll = max(0, s.current().scroll-page)
		return nil
	}

	if s.focus == focusBuddies {
		return s.handleBuddyKey(msg)
	}

	switch msg.String() {
	case "enter":
		line := strings.TrimSpace(s.input.Value())
		s.input.SetValue("")
		if line == "" {
			return nil
		}
		if strings.HasPrefix(line, "/") {
			return s.runCommand(line)
		}
		return s.sendToCurrent(line)
	case "esc":
		s.input.SetValue("")
		return nil
	}

	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return cmd
}

func (s *sessionModel) handleBuddyKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		s.moveBuddyCursor(-1)
		return nil
	case "down", "j":
		s.moveBuddyCursor(1)
		return nil
	}

	row, ok := s.selectedBuddy()
	if !ok {
		return nil
	}
	switch msg.String() {
	case "enter", "m":
		s.activate(s.imWindow(row.screenName))
		s.focusInput()
	case "i":
		return s.getInfo(row.screenName)
	case "w":
		return s.warn(row.screenName, false)
	case "W":
		return s.warn(row.screenName, true)
	case "b":
		if row.blocked || s.client.IsBlocked(row.screenName) {
			return s.unblock(row.screenName)
		}
		return s.block(row.screenName)
	case "d", "delete":
		if row.blocked {
			return s.unblock(row.screenName)
		}
		return s.removeBuddy(row.screenName)
	}
	return nil
}

func (s *sessionModel) updateEditor(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		s.editing = false
		s.input.Focus()
		s.status("Profile edit canceled.")
		return nil
	case "ctrl+s":
		profile := s.editor.Value()
		s.editing = false
		s.input.Focus()
		if err := s.client.SetProfile(profile); err != nil {
			s.notify("Unable to set profile: "+err.Error(), true)
			return nil
		}
		if a := s.account(); a != nil {
			a.Profile = profile
			if err := s.cfg.save(); err != nil {
				s.notify("Unable to save config: "+err.Error(), true)
			}
		}
		s.notify("Profile updated.", false)
		return nil
	}
	var cmd tea.Cmd
	s.editor, cmd = s.editor.Update(msg)
	return cmd
}

// mouse

// handleMouse clicks the zone under the mouse or scrolls with the wheel.
func (s *sessionModel) handleMouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress {
		return
	}
	switch msg.Button {
	case tea.MouseButtonLeft:
		for _, z := range s.zones {
			if msg.Y == z.y && msg.X >= z.x && msg.X < z.x+z.width {
				z.click()
				return
			}
		}
	case tea.MouseButtonWheelUp:
		if s.focus == focusBuddies {
			s.moveBuddyCursor(-1)
		} else {
			s.current().scroll += wheelLines
		}
	case tea.MouseButtonWheelDown:
		if s.focus == focusBuddies {
			s.moveBuddyCursor(1)
		} else {
			s.current().scroll = max(0, s.current().scroll-wheelLines)
		}
	}
}

// addZone makes a part of a screen line clickable.
func (s *sessionModel) addZone(x, y, width int, click func()) {
	s.zones = append(s.zones, zone{x: x, y: y, width: width, click: click})
}

func (s *sessionModel) focusInput() {
	s.focus = focusInput
	s.input.Focus()
}

// clickWindow switches to a window clicked in the tabs.
func (s *sessionModel) clickWindow(w *window) {
	s.activate(w)
	s.focusInput()
}

// clickBuddy selects a buddy clicked in the buddy list, or opens an IM with
// them if they're already selected.
func (s *sessionModel) clickBuddy(i int) {
	if s.focus == focusBuddies && s.buddyCursor == i {
		s.activate(s.imWindow(s.buddyRows[i].screenName))
		s.focusInput()
		return
	}
	s.buddyCursor = i
	s.focus = focusBuddies
	s.input.Blur()
}

// events

func (s *sessionModel) handleEvent(ev client.Event) tea.Cmd {
	var cmds []tea.Cmd

	switch ev := ev.(type) {
	case client.BuddyArrivedEvent:
		key := client.NormalizeScreenName(ev.Info.ScreenName)
		prev, wasOnline := s.online[key]
		s.online[key] = ev.Info
		var change string
		switch {
		case !wasOnline:
			change = "signed on"
		case prev.Away != ev.Info.Away && ev.Info.Away:
			change = "is away"
		case prev.Away != ev.Info.Away:
			change = "is back"
		}
		if change != "" {
			now, line := time.Now(), dimStyle.Render(ev.Info.ScreenName+" "+change)
			s.appendTo(s.windows[0], now, line)
			if w := s.find(imKey(ev.Info.ScreenName)); w != nil {
				w.addAt(now, line)
			}
		}
		s.refreshBuddies()
	case client.BuddyDepartedEvent:
		delete(s.online, client.NormalizeScreenName(ev.ScreenName))
		now, line := time.Now(), dimStyle.Render(ev.ScreenName+" signed off")
		s.appendTo(s.windows[0], now, line)
		if w := s.find(imKey(ev.ScreenName)); w != nil {
			w.addAt(now, line)
		}
		s.refreshBuddies()
	case client.IMEvent:
		w := s.imWindow(ev.From.ScreenName)
		w.title = ev.From.ScreenName
		name := peerStyle.Render(ev.From.ScreenName)
		if ev.AutoResponse {
			name += dimStyle.Render(" (auto-response)")
		}
		s.appendTo(w, ev.Time, fmt.Sprintf("%s: %s", name, s.render(ev.Message)))
		if away := s.client.AwayMessage(); away != "" && !ev.AutoResponse {
			key := client.NormalizeScreenName(ev.From.ScreenName)
			if !s.awayReplied[key] {
				s.awayReplied[key] = true
				cmds = append(cmds, s.sendIM(ev.From.ScreenName, away, true))
			}
		}
	case client.TypingEvent:
		// typing notifications aren't shown
	case client.WarnedEvent:
		by := "anonymously"
		if ev.By != "" {
			by = "by " + ev.By
		}
		s.notify(fmt.Sprintf("You were warned %s. Your warning level is now %d%%.", by, ev.NewLevel), true)
	case client.SelfInfoEvent:
		// shown in the header
	case client.BuddyListEvent:
		s.refreshBuddies()
	case client.RateLimitEvent:
		if ev.Limited && !s.rateLimited {
			s.notify("You are sending too fast. The server is dropping your messages; slow down.", true)
		} else if !ev.Limited && s.rateLimited {
			s.status("Rate limit cleared.")
		}
		s.rateLimited = ev.Limited
	case client.ChatInviteEvent:
		inv := ev.Invite
		s.lastInvite = &inv
		text := fmt.Sprintf("%s invited you to chat room %q", inv.From, inv.RoomName)
		if inv.Message != "" {
			text += ": " + htmlterm.Strip(inv.Message)
		}
		s.notify(text+" — type /accept to join.", false)
	case client.ChatJoinedEvent:
		w := s.open(&window{kind: winChat, key: "chat:" + ev.Room.ID(), title: "#" + ev.Room.Name()})
		w.room = ev.Room
		w.addAt(time.Now(), statusStyle.Render("You joined chat room "+ev.Room.Name()+"."))
		s.activate(w)
	case client.ChatMessageEvent:
		w := s.find("chat:" + ev.Room.ID())
		if w == nil {
			break
		}
		name := peerStyle.Render(ev.From)
		if client.NormalizeScreenName(ev.From) == client.NormalizeScreenName(s.screenName) {
			name = selfStyle.Render(ev.From)
		}
		s.appendTo(w, ev.Time, fmt.Sprintf("%s: %s", name, s.render(ev.Message)))
	case client.ChatUsersEvent:
		w := s.find("chat:" + ev.Room.ID())
		if w == nil {
			break
		}
		text := strings.Join(ev.Users, ", ") + " left"
		switch {
		case ev.Joined && !w.gotUsers:
			// the first arrival lists the users already in the room
			text = "In room: " + strings.Join(ev.Users, ", ")
			w.gotUsers = true
		case ev.Joined:
			text = strings.Join(ev.Users, ", ") + " joined"
		}
		w.addAt(time.Now(), dimStyle.Render(text))
	case client.ChatClosedEvent:
		w := s.find("chat:" + ev.Room.ID())
		if w == nil {
			break
		}
		w.room = nil
		text := "You left the chat room."
		if ev.Err != nil {
			text = "Disconnected from the chat room: " + ev.Err.Error()
		}
		s.appendTo(w, time.Now(), statusStyle.Render(text))
	case client.DisconnectedEvent:
		reason := "Disconnected."
		if ev.Err != nil && !errors.Is(ev.Err, client.ErrClosed) {
			reason = "Disconnected: " + ev.Err.Error()
		}
		return func() tea.Msg { return signedOffMsg{reason: reason} }
	}

	cmds = append(cmds, s.waitEvent())
	return tea.Batch(cmds...)
}

// requests

// do runs a request in the background and reports its result as a notice in
// the window with the given key.
func (s *sessionModel) do(key string, f func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		text, err := f(ctx)
		if err == nil && text == "" {
			return nil
		}
		return noticeMsg{key: key, text: text, err: err}
	}
}

// compose turns a typed message into HTML in the user's font style.
// Formatting tags typed into the message are kept.
func (s *sessionModel) compose(text string) string {
	msg := sanitizeHTML(text)
	if a := s.account(); a != nil {
		msg = a.Font.wrap(msg)
	}
	return msg
}

// sendToCurrent sends a line typed into the input to the active window.
func (s *sessionModel) sendToCurrent(text string) tea.Cmd {
	w := s.current()
	switch w.kind {
	case winIM:
		return s.sendIM(w.peer, s.compose(text), false)
	case winChat:
		if w.room == nil {
			s.appendTo(w, time.Time{}, errorStyle.Render("You are not in this chat room. Use /join to rejoin."))
			return nil
		}
		room, msg := w.room, s.compose(text)
		return s.do(w.key, func(context.Context) (string, error) {
			if err := room.Send(msg); err != nil {
				return "Message not sent", err
			}
			return "", nil
		})
	default:
		s.notify("Open a conversation first: select a buddy and press Enter, or type /im <screen name> <message>. /help lists commands.", true)
		return nil
	}
}

// sendIM sends an IM and shows it in the conversation window.
func (s *sessionModel) sendIM(to, message string, auto bool) tea.Cmd {
	w := s.imWindow(to)
	name := selfStyle.Render(s.screenName)
	if auto {
		name += dimStyle.Render(" (auto-response)")
	}
	w.addAt(time.Now(), fmt.Sprintf("%s: %s", name, s.render(message)))
	c := s.client
	return s.do(w.key, func(ctx context.Context) (string, error) {
		var err error
		if auto {
			err = c.SendAutoResponse(ctx, to, message)
		} else {
			err = c.SendIM(ctx, to, message)
		}
		if err != nil {
			return "Message not delivered", err
		}
		return "", nil
	})
}

func (s *sessionModel) getInfo(screenName string) tea.Cmd {
	c := s.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		p, err := c.UserInfo(ctx, screenName)
		return infoMsg{screenName: screenName, profile: p, err: err}
	}
}

func (s *sessionModel) showInfo(screenName string, p client.UserProfile) {
	key := "info:" + client.NormalizeScreenName(screenName)
	w := s.open(&window{kind: winInfo, key: key})
	w.title = "Info: " + p.Info.ScreenName
	w.peer = p.Info.ScreenName
	w.lines = nil
	w.scroll = 0

	field := func(label, value string) {
		w.add(groupStyle.Render(fmt.Sprintf("%-14s", label)) + value)
	}
	field("Screen name", p.Info.ScreenName)
	if !p.Info.SignonTime.IsZero() {
		field("Online since", p.Info.SignonTime.Format("Mon Jan 2 15:04"))
	}
	if p.Info.IdleMinutes > 0 {
		field("Idle", idleText(p.Info.IdleMinutes))
	}
	field("Warning level", fmt.Sprintf("%d%%", p.Info.WarningLevel))
	var class []string
	for _, f := range []struct {
		flag uint16
		name string
	}{
		{wire.OServiceUserFlagAdministrator, "administrator"},
		{wire.OServiceUserFlagUnconfirmed, "unconfirmed"},
		{wire.OServiceUserFlagICQ, "ICQ"},
		{wire.OServiceUserFlagBot, "bot"},
		{wire.OServiceUserFlagWireless, "mobile"},
	} {
		if p.Info.Flags&f.flag != 0 {
			class = append(class, f.name)
		}
	}
	if len(class) > 0 {
		field("Account", strings.Join(class, ", "))
	}
	if p.Info.Away {
		field("Away message", "")
		w.add(s.render(p.AwayMessage))
	}
	w.add("")
	w.add(groupStyle.Render("Profile"))
	if strings.TrimSpace(p.Profile) == "" {
		w.add(dimStyle.Render("(no profile)"))
	} else {
		w.add(s.render(p.Profile))
	}
	s.activate(w)
}

func (s *sessionModel) warn(screenName string, anon bool) tea.Cmd {
	c := s.client
	return s.do("", func(ctx context.Context) (string, error) {
		res, err := c.Warn(ctx, screenName, anon)
		if err != nil {
			return "Unable to warn " + screenName + " (you can only warn users who recently messaged you)", err
		}
		kind := "Warned"
		if anon {
			kind = "Anonymously warned"
		}
		return fmt.Sprintf("%s %s. Their warning level is now %d%%.", kind, screenName, res.NewLevel), nil
	})
}

func (s *sessionModel) block(screenName string) tea.Cmd {
	c := s.client
	return s.do("", func(ctx context.Context) (string, error) {
		if err := c.Block(ctx, screenName); err != nil {
			return "Unable to block " + screenName, err
		}
		return "Blocked " + screenName + ".", nil
	})
}

func (s *sessionModel) unblock(screenName string) tea.Cmd {
	c := s.client
	return s.do("", func(ctx context.Context) (string, error) {
		if err := c.Unblock(ctx, screenName); err != nil {
			return "Unable to unblock " + screenName, err
		}
		return "Unblocked " + screenName + ".", nil
	})
}

func (s *sessionModel) addBuddy(group, screenName string) tea.Cmd {
	c := s.client
	return s.do("", func(ctx context.Context) (string, error) {
		if err := c.AddBuddy(ctx, group, screenName); err != nil {
			return "Unable to add " + screenName, err
		}
		if group == "" {
			group = client.DefaultBuddyGroup
		}
		return fmt.Sprintf("Added %s to %s.", screenName, group), nil
	})
}

func (s *sessionModel) removeBuddy(screenName string) tea.Cmd {
	c := s.client
	return s.do("", func(ctx context.Context) (string, error) {
		if err := c.RemoveBuddy(ctx, screenName); err != nil {
			return "Unable to remove " + screenName, err
		}
		return "Removed " + screenName + " from your buddy list.", nil
	})
}

func (s *sessionModel) joinChat(name string) tea.Cmd {
	c := s.client
	return s.do("", func(ctx context.Context) (string, error) {
		if _, err := c.JoinChat(ctx, name); err != nil {
			return "Unable to join chat room " + name, err
		}
		return "", nil
	})
}

func (s *sessionModel) acceptInvite(inv client.ChatInvite) tea.Cmd {
	c := s.client
	return s.do("", func(ctx context.Context) (string, error) {
		if _, err := c.AcceptChatInvite(ctx, inv); err != nil {
			return "Unable to join chat room " + inv.RoomName, err
		}
		return "", nil
	})
}

// view

func (s *sessionModel) view(width, height int) string {
	lay := newLayout(width, height)
	s.zones = s.zones[:0]
	var top, bottom []string
	if lay.header {
		top = append(top, s.headerView(width))
	}
	bottom = append(bottom, s.inputView(width))
	if lay.help {
		bottom = append(bottom, dimStyle.Render(fit(width, s.helpLines()...)))
	}
	lines, inputY := top, len(top)
	if bodyHeight := height - len(top) - len(bottom); bodyHeight > 0 {
		lines = append(lines, s.bodyView(lay, len(top), width, bodyHeight))
		inputY += bodyHeight
	}
	s.addZone(0, inputY, width, s.focusInput)
	return strings.Join(append(lines, bottom...), "\n")
}

// bodyView renders the buddy list and the main pane in height lines, starting
// at screen line y.
func (s *sessionModel) bodyView(lay layout, y, width, height int) string {
	innerHeight, mainWidth, border := height, width, 0
	if lay.borders {
		innerHeight, mainWidth, border = height-2, width-2, 1
	}
	var side string
	if lay.buddyWidth > 0 {
		side = s.buddyPaneView(lay, border, y+border, innerHeight)
		mainWidth -= lipgloss.Width(side)
	}
	mainX, mainY := lipgloss.Width(side)+border, y+border

	mainFocused := s.focus == focusInput || s.editing
	var content string
	switch {
	case s.editing:
		content = s.editorView(mainWidth, innerHeight)
	case s.focus == focusBuddies && lay.buddyWidth == 0:
		// no room beside the main pane: the buddy list takes its place
		content = s.buddyListView(mainX, mainY, mainWidth, innerHeight)
		mainFocused = true
	default:
		content = s.windowView(lay, mainX, mainY, mainWidth, innerHeight)
	}
	style := lipgloss.NewStyle()
	if lay.borders {
		style = paneStyle
		if mainFocused {
			style = focusedPaneStyle
		}
	}
	main := style.Width(mainWidth).Height(innerHeight).Render(content)
	if side == "" {
		return main
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, side, main)
}

func (s *sessionModel) headerView(width int) string {
	self := s.client.Self()
	join := func(parts ...string) string {
		var nonEmpty []string
		for _, p := range parts {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		return strings.Join(nonEmpty, dimStyle.Render(" · "))
	}

	// the short forms leave out what's usual: no warning, being available
	name := selfStyle.Render(s.screenName)
	warn, shortWarn := dimStyle.Render(fmt.Sprintf("warning %d%%", self.WarningLevel)), ""
	if self.WarningLevel > 0 {
		warn = errorStyle.Render(fmt.Sprintf("warning %d%%", self.WarningLevel))
		shortWarn = errorStyle.Render(fmt.Sprintf("%d%%", self.WarningLevel))
	}
	state, shortState := onlineStyle.Render("available"), ""
	if s.client.AwayMessage() != "" {
		state = awayStyle.Render("away")
		shortState = state
	}
	var rate string
	if s.rateLimited {
		rate = errorStyle.Render("rate limited")
	}

	left := " " + join(titleStyle.Render("Go OSCAR"), name, warn, state, rate)
	right := dimStyle.Render(s.server + " ")
	if gap := width - lipgloss.Width(left) - lipgloss.Width(right); gap >= 1 {
		return left + strings.Repeat(" ", gap) + right
	}
	short := join(name, shortWarn, shortState, rate)
	return fit(width, left, " "+short, short)
}

// buddyPaneView renders the buddy list beside the main pane, with its
// content at screen position x, y.
func (s *sessionModel) buddyPaneView(lay layout, x, y, height int) string {
	style := separatorStyle
	if lay.borders {
		style = paneStyle
	}
	if s.focus == focusBuddies {
		style = style.BorderForeground(accentColor)
	}
	return style.Width(lay.buddyWidth).Height(height).Render(s.buddyListView(x, y, lay.buddyWidth, height))
}

// buddyListView renders the buddy list in height lines at screen position x,
// y, scrolled to the cursor.
func (s *sessionModel) buddyListView(x, y, width, height int) string {
	var lines []string
	if height >= 3 {
		title := fit(width, "Buddy List", "Buddies")
		if s.focus == focusBuddies {
			title = focusedLabelStyle.Render(title)
		} else {
			title = dimStyle.Render(title)
		}
		lines = append(lines, title)
	}

	visible := height - len(lines)
	start := max(0, s.buddyCursor-visible+1)
	for i := start; i < len(s.buddyRows) && len(lines) < height; i++ {
		row := s.buddyRows[i]
		text := row.text
		if row.screenName != "" {
			text = " " + text
		}
		if row.wrap {
			wrapped := strings.Split(lipgloss.NewStyle().Width(width).Render(text), "\n")
			lines = append(lines, wrapped[:min(len(wrapped), height-len(lines))]...)
			continue
		}
		if row.screenName != "" {
			s.addZone(x, y+len(lines), width, func() { s.clickBuddy(i) })
		}
		text = truncate(text, width)
		if i == s.buddyCursor && s.focus == focusBuddies && row.screenName != "" {
			text = selectedStyle.Render(text + strings.Repeat(" ", max(0, width-lipgloss.Width(text))))
		}
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

// tabsView renders the window tabs at screen position x, y, or only the
// active window's if they don't all fit. Clicking a tab switches to its
// window; clicking the active window's tab alone switches to the next one.
func (s *sessionModel) tabsView(x, y, width int) string {
	sep := dimStyle.Render(" │ ")
	var tabs []string
	var unread string
	for i, w := range s.windows {
		title := w.title
		switch {
		case i == s.active:
			tabs = append(tabs, activeTabStyle.Render(title))
		case w.unread:
			tabs = append(tabs, unreadTabStyle.Render(title+"*"))
			unread = unreadTabStyle.Render(" *")
		default:
			tabs = append(tabs, inactiveTabStyle.Render(title))
		}
	}
	if full := strings.Join(tabs, sep); lipgloss.Width(full) <= width {
		for i, tab := range tabs {
			w := s.windows[i]
			s.addZone(x, y, lipgloss.Width(tab), func() { s.clickWindow(w) })
			x += lipgloss.Width(tab) + lipgloss.Width(sep)
		}
		return full
	}

	next := s.windows[(s.active+1)%len(s.windows)]
	s.addZone(x, y, width, func() { s.clickWindow(next) })
	title := s.current().title
	pos := dimStyle.Render(fmt.Sprintf("%d/%d ", s.active+1, len(s.windows)))
	return fit(width,
		pos+activeTabStyle.Render(title)+unread,
		activeTabStyle.Render(truncate(title, width-lipgloss.Width(unread)))+unread,
	)
}

// subLine describes the active window under the tabs.
func (s *sessionModel) subLine(w *window) string {
	if w.scroll > 0 {
		return fmt.Sprintf("scrolled up %d lines (PgDn to return)", w.scroll)
	}
	switch w.kind {
	case winChat:
		if w.room != nil {
			return fmt.Sprintf("%d in room: %s", len(w.room.Users()), strings.Join(w.room.Users(), ", "))
		}
	case winIM:
		if info, ok := s.online[client.NormalizeScreenName(w.peer)]; ok {
			if info.Away {
				return info.ScreenName + " is online (away)"
			}
			return info.ScreenName + " is online"
		}
		if s.client.IsBuddy(w.peer) {
			// presence is only tracked for buddies, so a non-buddy's
			// status is unknown
			return w.peer + " is offline"
		}
	}
	return ""
}

// windowView renders the active window at screen position x, y.
func (s *sessionModel) windowView(lay layout, x, y, width, height int) string {
	w := s.current()
	var headRows int
	switch {
	case height >= subLineHeight:
		headRows = 2 // tabs and sub line
	case height >= 2:
		headRows = 1 // tabs
	}
	contentHeight := height - headRows

	// wrap lines to the pane width
	wrap := lipgloss.NewStyle().Width(width)
	var rows []string
	for _, l := range w.lines {
		text := l.text
		if lay.timestamps && !l.at.IsZero() {
			text = timestamp(l.at) + " " + text
		}
		rows = append(rows, strings.Split(wrap.Render(text), "\n")...)
	}

	maxScroll := max(0, len(rows)-contentHeight)
	if w.scroll > maxScroll {
		w.scroll = maxScroll
	}
	end := len(rows) - w.scroll
	start := max(0, end-contentHeight)
	// pad so the content sits at the bottom of the pane, like a chat log
	lines := padLines(rows[start:end], contentHeight)

	switch headRows {
	case 2:
		lines = append([]string{s.tabsView(x, y, width), truncate(dimStyle.Render(s.subLine(w)), width)}, lines...)
	case 1:
		var scrolled string
		if w.scroll > 0 {
			scrolled = dimStyle.Render(fmt.Sprintf(" ↑%d", w.scroll))
		}
		lines = append([]string{s.tabsView(x, y, width-lipgloss.Width(scrolled)) + scrolled}, lines...)
	}
	return strings.Join(lines, "\n")
}

func (s *sessionModel) editorView(width, height int) string {
	var lines []string
	if height >= 3 {
		lines = append(lines, fit(width,
			titleStyle.Render("Edit profile")+dimStyle.Render("  Ctrl+S: save • Esc: cancel"),
			titleStyle.Render("Profile")+dimStyle.Render(" ^S: save • Esc"),
			dimStyle.Render("^S: save • Esc"),
		))
	}

	// the preview needs room below the editor
	rest := height - len(lines)
	editorHeight, previewHeight := rest, 0
	if rest >= 8 {
		editorHeight = rest / 2
		previewHeight = rest - editorHeight - 1
	}
	s.editor.SetWidth(width)
	s.editor.SetHeight(max(1, editorHeight))
	lines = append(lines, s.editor.View())

	if previewHeight > 0 {
		preview := lipgloss.NewStyle().Width(width).Render(s.render(s.editor.Value()))
		previewLines := strings.Split(preview, "\n")
		lines = append(lines, groupStyle.Render("Preview"))
		lines = append(lines, previewLines[:min(len(previewLines), previewHeight)]...)
	}
	return strings.Join(lines, "\n")
}

func (s *sessionModel) inputView(width int) string {
	w := s.current()
	var prompt, name string
	switch w.kind {
	case winIM:
		prompt, name = " to "+w.peer+"> ", w.peer
	case winChat:
		prompt, name = " "+w.title+"> ", w.title
	default:
		prompt = " command> "
	}
	// the prompt gives way to the text being typed
	limit := width / 3
	if name != "" && lipgloss.Width(prompt) > limit && limit >= 6 {
		prompt = name + "> "
		if lipgloss.Width(prompt) > limit {
			prompt = truncate(name, limit-3) + "…> "
		}
	}
	prompt = fit(limit, prompt, "> ")
	if s.focus == focusInput {
		prompt = focusedLabelStyle.Render(prompt)
	} else {
		prompt = dimStyle.Render(prompt)
	}
	s.input.Width = max(1, width-lipgloss.Width(prompt)-1)
	return prompt + s.input.View()
}

// helpLines are the key help line, from longest to shortest.
func (s *sessionModel) helpLines() []string {
	if s.focus == focusBuddies {
		return []string{
			" ↑/↓: select • Enter: IM • i: info • w/W: warn/anon • b: block • d: remove • Tab: input",
			" Enter: IM • i: info • w: warn • b: block • d: remove • Tab: input",
			" Enter/i/w/b/d • Tab: input",
			" Tab: input",
		}
	}
	return []string{
		" Tab: buddy list • Ctrl+N/P: switch window • Ctrl+W: close • PgUp/PgDn: scroll • /help • Ctrl+C: quit",
		" Tab: buddies • ^N/^P: windows • ^W: close • /help",
		" Tab: buddies • /help",
		" /help",
	}
}
