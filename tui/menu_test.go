package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

// press sends a key to s as if it were pressed.
func press(s *sessionModel, key string) tea.Cmd {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "f10":
		msg = tea.KeyMsg{Type: tea.KeyF10}
	}
	return s.update(msg, 100, 24)
}

func TestSessionView_menu(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		// open opens the menu, by clicking or with a key.
		open       func(t *testing.T, s *sessionModel)
		choose     func(t *testing.T, s *sessionModel)
		wantPrompt string
	}{
		{
			name: "click the title and an item", width: 100, height: 24,
			open:       func(t *testing.T, s *sessionModel) { click(t, s, 100, 24, "Go OSCAR") },
			choose:     func(t *testing.T, s *sessionModel) { click(t, s, 100, 24, "Add buddy…") },
			wantPrompt: "Add buddy",
		},
		{
			name: "keyboard", width: 100, height: 24,
			open: func(t *testing.T, s *sessionModel) { press(s, "f10") },
			choose: func(t *testing.T, s *sessionModel) {
				press(s, "down")
				press(s, "enter")
			},
			wantPrompt: "Add buddy",
		},
		{
			name: "click the key help on a short terminal", width: 100, height: 13,
			open:       func(t *testing.T, s *sessionModel) { click(t, s, 100, 13, "F10: menu") },
			choose:     func(t *testing.T, s *sessionModel) { click(t, s, 100, 13, "Join chat room…") },
			wantPrompt: "Join room",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession()
			s.input.SetValue("a draft")
			tt.open(t, s)
			assert.True(t, s.menuOpen)
			view := s.view(tt.width, tt.height)
			assertFits(t, view, tt.width, tt.height)
			assert.Contains(t, view, "Send IM…")

			tt.choose(t, s)
			assert.False(t, s.menuOpen)
			if assert.NotNil(t, s.prompt) {
				assert.Equal(t, tt.wantPrompt, s.prompt.label)
			}
			assert.Equal(t, focusInput, s.focus)
			assert.Contains(t, s.view(tt.width, tt.height), tt.wantPrompt+">")
		})
	}
}

func TestSessionView_menuCloses(t *testing.T) {
	tests := []struct {
		name  string
		close func(t *testing.T, s *sessionModel)
	}{
		{name: "click outside", close: func(t *testing.T, s *sessionModel) { click(t, s, 100, 24, "message 29") }},
		{name: "click the title", close: func(t *testing.T, s *sessionModel) { click(t, s, 100, 24, "Go OSCAR") }},
		{name: "escape", close: func(t *testing.T, s *sessionModel) { press(s, "esc") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession()
			s.openMenu()
			tt.close(t, s)
			assert.False(t, s.menuOpen)
			assert.Equal(t, "SomeBuddyWithALongName", s.current().title)
			assert.NotContains(t, s.view(100, 24), "Sign off")
		})
	}
}

func TestSessionView_menuWindowActions(t *testing.T) {
	s := newTestSession()
	s.openMenu()
	view := s.view(100, 40)
	// the IM window's correspondent isn't a buddy
	for _, item := range []string{"View info", "Add to buddy list", "Block", "Warn anonymously", "Close window"} {
		assert.Contains(t, view, item)
	}

	click(t, s, 100, 40, "Close window")
	assert.Equal(t, "Info: bob", s.current().title)
}

func TestSessionView_prompt(t *testing.T) {
	s := newTestSession()
	s.input.SetValue("a draft")
	s.askIM()
	assert.Equal(t, "", s.input.Value())

	s.input.SetValue("carol")
	press(s, "enter")
	assert.Equal(t, "carol", s.current().title)
	assert.Nil(t, s.prompt)
	assert.Equal(t, "a draft", s.input.Value())

	s.askIM()
	press(s, "esc")
	assert.Nil(t, s.prompt)
	assert.Equal(t, "carol", s.current().title)
	assert.Equal(t, "a draft", s.input.Value())
}

func TestSessionView_addBuddyButton(t *testing.T) {
	tests := []struct {
		name   string
		choose func(t *testing.T, s *sessionModel)
	}{
		{name: "click", choose: func(t *testing.T, s *sessionModel) { click(t, s, 100, 24, "+ Add buddy") }},
		{name: "keyboard", choose: func(t *testing.T, s *sessionModel) {
			press(s, "tab")
			press(s, "down")
			press(s, "down")
			assert.Contains(t, s.view(100, 24), "Enter: add buddy")
			press(s, "enter")
		}},
		{name: "key", choose: func(t *testing.T, s *sessionModel) {
			press(s, "tab")
			press(s, "a")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession()
			s.buddyRows = append(s.buddyRows, buddyRow{text: "+ Add buddy", action: s.askAddBuddy, label: "add buddy"})
			tt.choose(t, s)
			if assert.NotNil(t, s.prompt) {
				assert.Equal(t, "Add buddy", s.prompt.label)
			}
			assert.Equal(t, focusInput, s.focus)
		})
	}
}

func TestSessionView_clickHelp(t *testing.T) {
	tests := []struct {
		name       string
		focus      int
		click      string
		wantCmd    bool
		wantFocus  int
		wantWindow string
	}{
		{name: "buddy info", focus: focusBuddies, click: "i: info", wantCmd: true, wantFocus: focusBuddies, wantWindow: "SomeBuddyWithALongName"},
		{name: "buddy IM", focus: focusBuddies, click: "Enter: IM", wantFocus: focusInput, wantWindow: "SomeBuddyWithALongName"},
		{name: "back to input", focus: focusBuddies, click: "Tab: input", wantFocus: focusInput, wantWindow: "SomeBuddyWithALongName"},
		{name: "to the buddy list", focus: focusInput, click: "Tab: buddy list", wantFocus: focusBuddies, wantWindow: "SomeBuddyWithALongName"},
		{name: "next window", focus: focusInput, click: "Ctrl+N/P", wantFocus: focusInput, wantWindow: "Info: bob"},
		{name: "close window", focus: focusInput, click: "Ctrl+W", wantFocus: focusInput, wantWindow: "Info: bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession()
			s.focus = tt.focus
			cmd := click(t, s, 120, 24, tt.click)
			assert.Equal(t, tt.wantCmd, cmd != nil)
			assert.Equal(t, tt.wantFocus, s.focus)
			assert.Equal(t, tt.wantWindow, s.current().title)
		})
	}
}

func TestSessionView_windowLinks(t *testing.T) {
	s := newTestSession()
	view := s.view(100, 24)
	assert.Contains(t, view, "Info · Add buddy · Block")
	assert.NotContains(t, view, "Warn")
	assert.NotNil(t, click(t, s, 100, 24, "Add buddy ·"))

	// scrolled up, the line under the tabs returns to the bottom
	s.current().scroll = 5
	click(t, s, 100, 24, "scrolled up")
	assert.Equal(t, 0, s.current().scroll)
	assert.True(t, strings.Contains(s.view(100, 24), "message 29"))
}
