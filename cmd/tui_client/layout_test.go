package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"

	"github.com/kirsle/go-oscar-client/client"
)

// sizes are the terminal sizes the screens are rendered at, from the
// smallest supported up.
var sizes = func() [][2]int {
	var sizes [][2]int
	for _, w := range []int{16, 20, 30, 39, 40, 49, 50, 63, 64, 80, 89, 90, 120} {
		for _, h := range []int{2, 3, 4, 5, 7, 8, 11, 12, 13, 24, 40} {
			sizes = append(sizes, [2]int{w, h})
		}
	}
	return sizes
}()

// assertFits checks that view fits in a terminal of width by height.
func assertFits(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	assert.LessOrEqual(t, len(lines), height, "too many lines:\n%s", view)
	for i, line := range lines {
		assert.LessOrEqual(t, lipgloss.Width(line), width, "line %d too wide: %q", i, line)
	}
}

func newTestSession() *sessionModel {
	s := newSessionModel(&config{}, signedOnMsg{
		client:     &client.Client{},
		server:     "127.0.0.1:5190",
		screenName: "alice",
	}, true)
	im := s.imWindow("SomeBuddyWithALongName")
	for i := range 30 {
		im.addAt(time.Now(), fmt.Sprintf("%s: message %d with enough words in it to wrap on a narrow terminal", peerStyle.Render("SomeBuddyWithALongName"), i))
	}
	s.open(&window{kind: winInfo, key: "info:bob", title: "Info: bob", peer: "bob", unread: true})
	s.activate(im)
	s.buddyRows = []buddyRow{
		{text: groupStyle.Render("Buddies (1/2)")},
		{text: onlineStyle.Render("● ") + "SomeBuddyWithALongName", screenName: "SomeBuddyWithALongName"},
		{text: dimStyle.Render("○ bob"), screenName: "bob"},
	}
	s.buddyCursor = 1
	return s
}

func TestSessionView_fits(t *testing.T) {
	for _, state := range []string{"input", "buddies", "editing", "scrolled"} {
		for _, size := range sizes {
			width, height := size[0], size[1]
			t.Run(fmt.Sprintf("%s %dx%d", state, width, height), func(t *testing.T) {
				s := newTestSession()
				switch state {
				case "buddies":
					s.focus = focusBuddies
				case "editing":
					s.editing = true
					s.editor.SetValue("my <b>profile</b>")
				case "scrolled":
					s.current().scroll = 5
				}
				view := s.view(width, height)
				assertFits(t, view, width, height)
				assert.Equal(t, height, lipgloss.Height(view))
			})
		}
	}
}

func TestSessionView_buddyList(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		focus         int
		wantBuddies   bool
		wantWindow    bool
	}{
		{name: "wide terminal shows both", width: 100, height: 24, focus: focusInput, wantBuddies: true, wantWindow: true},
		{name: "medium terminal shows both", width: 64, height: 24, focus: focusInput, wantBuddies: true, wantWindow: true},
		{name: "narrow terminal hides the buddy list", width: 40, height: 24, focus: focusInput, wantWindow: true},
		{name: "narrow terminal shows the focused buddy list instead", width: 40, height: 24, focus: focusBuddies, wantBuddies: true},
		{name: "tiny terminal shows the focused buddy row", width: 16, height: 2, focus: focusBuddies, wantBuddies: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession()
			s.focus = tt.focus
			view := s.view(tt.width, tt.height)
			assert.Equal(t, tt.wantBuddies, strings.Contains(view, "● Some"), view)
			assert.Equal(t, tt.wantWindow, strings.Contains(view, "message 29"), view)
		})
	}
}

func TestLoginView_fits(t *testing.T) {
	for _, accounts := range []int{0, 3} {
		for _, status := range []string{"", "Sign on failed: dial tcp 127.0.0.1:5190: connect: connection refused"} {
			for _, size := range sizes {
				width, height := size[0], size[1]
				t.Run(fmt.Sprintf("%d accounts %q %dx%d", accounts, status, width, height), func(t *testing.T) {
					cfg := &config{}
					for i := range accounts {
						cfg.Accounts = append(cfg.Accounts, account{Server: "127.0.0.1:5190", ScreenName: fmt.Sprintf("user%d", i), Password: "x"})
					}
					l := newLoginModel(cfg, nil, loginArgs{})
					l.status = status
					view := l.view(width, height)
					assertFits(t, view, width, height)
				})
			}
		}
	}
}

func TestSessionView_emptyBuddyListHint(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		focus         int
	}{
		{name: "full width buddy list", width: wideWidth, height: 24, focus: focusInput},
		{name: "reduced width buddy list", width: mediumWidth, height: 24, focus: focusInput},
		{name: "buddy list in place of the main pane", width: 16, height: 8, focus: focusBuddies},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSessionModel(&config{}, signedOnMsg{client: &client.Client{}, screenName: "alice"}, true)
			s.focus = tt.focus
			view := s.view(tt.width, tt.height)
			assertFits(t, view, tt.width, tt.height)
			// the hint is wrapped, not cut off
			assert.Contains(t, view, "/add", view)
			assert.Contains(t, view, "<name>", view)
		})
	}
}

// click renders s and clicks the first place text is shown.
func click(t *testing.T, s *sessionModel, width, height int, text string) {
	t.Helper()
	for y, line := range strings.Split(s.view(width, height), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			s.handleMouse(tea.MouseMsg{X: lipgloss.Width(line[:i]), Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			return
		}
	}
	t.Fatalf("%q not shown", text)
}

func TestSessionView_click(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		clicks        []string
		wantWindow    string
		wantFocus     int
	}{
		{name: "tab with borders", width: 100, height: 24, clicks: []string{"Status"}, wantWindow: "Status", wantFocus: focusInput},
		{name: "tab without borders", width: 100, height: 10, clicks: []string{"Info: bob"}, wantWindow: "Info: bob", wantFocus: focusInput},
		{name: "collapsed tabs switch to the next window", width: 45, height: 12, clicks: []string{"2/3"}, wantWindow: "Info: bob", wantFocus: focusInput},
		{name: "buddy is selected", width: 100, height: 24, clicks: []string{"○ bob"}, wantWindow: "SomeBuddyWithALongName", wantFocus: focusBuddies},
		{name: "selected buddy is messaged", width: 100, height: 24, clicks: []string{"○ bob", "○ bob"}, wantWindow: "bob", wantFocus: focusInput},
		{name: "input is focused", width: 100, height: 24, clicks: []string{"○ bob", "to Some"}, wantWindow: "SomeBuddyWithALongName", wantFocus: focusInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession()
			for _, text := range tt.clicks {
				click(t, s, tt.width, tt.height, text)
			}
			assert.Equal(t, tt.wantWindow, s.current().title)
			assert.Equal(t, tt.wantFocus, s.focus)
		})
	}
}

func TestLoginView_box(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		wantBox       bool
	}{
		{name: "default terminal", width: 80, height: 24, wantBox: true},
		{name: "a little smaller than default", width: 78, height: 20, wantBox: true},
		{name: "half size", width: 40, height: 14, wantBox: true},
		{name: "smallest box", width: 34, height: 12, wantBox: true},
		{name: "too short for the box", width: 80, height: 9},
		{name: "too narrow for the box", width: 24, height: 24},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config{}
			for i := range 3 {
				cfg.Accounts = append(cfg.Accounts, account{Server: "127.0.0.1:5190", ScreenName: fmt.Sprintf("user%d", i), Password: "x"})
			}
			l := newLoginModel(cfg, nil, loginArgs{})
			l.status = "Sign on failed: connection refused"
			view := l.view(tt.width, tt.height)
			assertFits(t, view, tt.width, tt.height)
			assert.Equal(t, tt.wantBox, strings.Contains(view, "╭"), view)
		})
	}
}
