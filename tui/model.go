package tui

import (
	"crypto/tls"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"
)

// LoginArgs are the sign-on settings given on the command line.
type LoginArgs struct {
	Server, ScreenName, Password string
	// SSL requires or disables SSL for the sign-on, overriding the saved
	// account. It's nil if not given.
	SSL *bool
	// TLSConfig is the TLS config for SSL connections.
	TLSConfig *tls.Config
}

// model is the root of the TUI. It shows the sign-on screen until the user
// signs on, then the session screen.
type model struct {
	cfg    *Config
	logger *slog.Logger
	plain  bool

	width, height int

	login   *loginModel
	session *sessionModel
}

// New returns the root model of the TUI, which starts at the sign-on screen.
// If args has the server, screen name and password, it signs on right away.
func New(cfg *Config, logger *slog.Logger, plain bool, args LoginArgs) tea.Model {
	return model{
		cfg:    cfg,
		logger: logger,
		plain:  plain,
		login:  newLoginModel(cfg, logger, args),
	}
}

func (m model) Init() tea.Cmd {
	return m.login.init()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			if m.session != nil {
				m.session.close()
			}
			return m, tea.Quit
		}
	case signedOnMsg:
		m.login.signedOn(msg)
		m.session = newSessionModel(m.cfg, msg, m.plain)
		return m, m.session.init()
	case signedOffMsg:
		if m.session != nil {
			m.session.close()
			m.session = nil
		}
		m.login.signedOff(msg.reason)
		return m, m.login.init()
	}

	var cmd tea.Cmd
	if m.session != nil {
		cmd = m.session.update(msg, m.width, m.height)
	} else {
		cmd = m.login.update(msg)
	}
	return m, cmd
}

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.session != nil {
		return m.session.view(m.width, m.height)
	}
	return m.login.view(m.width, m.height)
}
