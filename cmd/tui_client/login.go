package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kirsle/go-oscar-client/client"
)

// clientIdentity is the client name reported to the server.
const clientIdentity = "Go OSCAR TUI"

// signOnTimeout bounds how long signing on may take.
const signOnTimeout = 30 * time.Second

// signedOnMsg reports a successful signon.
type signedOnMsg struct {
	client       *client.Client
	server       string
	screenName   string
	password     string
	savePassword bool
	ssl          bool
}

// loginFailedMsg reports a failed signon.
type loginFailedMsg struct {
	err error
}

// signedOffMsg returns to the sign-on screen.
type signedOffMsg struct {
	reason string
}

// Fields of the sign-on form, in focus order.
const (
	focusAccounts = iota
	focusServer
	focusScreenName
	focusPassword
	focusSavePassword
	focusSSL
	focusCount
)

// loginModel is the sign-on screen.
type loginModel struct {
	cfg    *config
	logger *slog.Logger

	cursor       int
	inputs       [focusCount]textinput.Model
	savePassword bool
	ssl          bool
	focus        int
	tlsConfig    *tls.Config

	busy       bool
	status     string
	autoSignon bool
}

func newLoginModel(cfg *config, logger *slog.Logger, args loginArgs) *loginModel {
	// SSL is required unless a saved account or the command line says
	// otherwise
	l := &loginModel{cfg: cfg, logger: logger, ssl: true, tlsConfig: args.tlsConfig}
	for i := focusServer; i <= focusPassword; i++ {
		in := textinput.New()
		in.Prompt = ""
		in.CharLimit = 64
		l.inputs[i] = in
	}
	l.inputs[focusServer].Placeholder = client.DefaultServer
	l.inputs[focusScreenName].Placeholder = "screen name"
	l.inputs[focusPassword].Placeholder = "password"
	l.inputs[focusPassword].EchoMode = textinput.EchoPassword
	l.inputs[focusPassword].EchoCharacter = '•'

	l.cursor = cfg.last()
	if l.cursor < 0 && len(cfg.Accounts) > 0 {
		l.cursor = 0
	}
	if l.cursor >= 0 {
		l.selectAccount(l.cursor)
	} else {
		l.inputs[focusServer].SetValue(client.DefaultServer)
	}

	// command line flags override the saved account
	if args.server != "" {
		l.inputs[focusServer].SetValue(args.server)
	}
	if args.screenName != "" {
		l.inputs[focusScreenName].SetValue(args.screenName)
		if args.password == "" {
			l.inputs[focusPassword].SetValue("")
			if a := cfg.find(l.value(focusServer), args.screenName); a != nil {
				l.inputs[focusPassword].SetValue(a.Password)
			}
		}
	}
	if args.password != "" {
		l.inputs[focusPassword].SetValue(args.password)
	}
	if args.ssl != nil {
		l.ssl = *args.ssl
	}
	l.autoSignon = args.screenName != "" && l.value(focusPassword) != ""

	switch {
	case l.value(focusScreenName) == "":
		l.setFocus(focusScreenName)
	default:
		l.setFocus(focusPassword)
	}
	return l
}

func (l *loginModel) init() tea.Cmd {
	if l.autoSignon {
		l.autoSignon = false
		return l.signOn()
	}
	return textinput.Blink
}

func (l *loginModel) value(field int) string {
	return strings.TrimSpace(l.inputs[field].Value())
}

// selectAccount fills the form from saved account i.
func (l *loginModel) selectAccount(i int) {
	a := l.cfg.Accounts[i]
	l.inputs[focusServer].SetValue(a.Server)
	l.inputs[focusScreenName].SetValue(a.ScreenName)
	l.inputs[focusPassword].SetValue(a.Password)
	l.savePassword = a.Password != ""
	l.ssl = a.SSL
}

func (l *loginModel) setFocus(f int) {
	if f == focusAccounts && len(l.cfg.Accounts) == 0 {
		f = focusServer
	}
	l.focus = f
	for i := focusServer; i <= focusPassword; i++ {
		if i == f {
			l.inputs[i].Focus()
		} else {
			l.inputs[i].Blur()
		}
	}
}

func (l *loginModel) moveFocus(delta int) {
	f := (l.focus + delta + focusCount) % focusCount
	if f == focusAccounts && len(l.cfg.Accounts) == 0 {
		f = (f + delta + focusCount) % focusCount
	}
	l.setFocus(f)
}

// signOn validates the form and starts signing on.
func (l *loginModel) signOn() tea.Cmd {
	server := l.value(focusServer)
	if server == "" {
		server = client.DefaultServer
		l.inputs[focusServer].SetValue(server)
	}
	screenName := l.value(focusScreenName)
	password := l.inputs[focusPassword].Value()
	if screenName == "" || password == "" {
		l.status = "Enter a screen name and password."
		return nil
	}

	var profile string
	if a := l.cfg.find(server, screenName); a != nil {
		profile = a.Profile
	}

	l.busy = true
	l.status = fmt.Sprintf("Signing on to %s as %s...", server, screenName)
	if !l.ssl {
		l.status = fmt.Sprintf("Signing on to %s as %s without SSL...", server, screenName)
	}
	savePassword := l.savePassword
	ssl := l.ssl
	tlsConfig := l.tlsConfig
	logger := l.logger
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), signOnTimeout)
		defer cancel()
		c, err := client.Dial(ctx, client.Config{
			Server:         server,
			ScreenName:     screenName,
			Password:       password,
			ClientIdentity: clientIdentity,
			Profile:        profile,
			SSL:            ssl,
			TLSConfig:      tlsConfig,
			Logger:         logger,
		})
		if err != nil {
			return loginFailedMsg{err: err}
		}
		return signedOnMsg{
			client:       c,
			server:       server,
			screenName:   screenName,
			password:     password,
			savePassword: savePassword,
			ssl:          ssl,
		}
	}
}

// signedOn saves the account that signed on.
func (l *loginModel) signedOn(msg signedOnMsg) {
	l.busy = false
	l.status = ""
	l.cfg.remember(msg.server, msg.screenName, msg.password, msg.savePassword, msg.ssl)
	l.cursor = l.cfg.last()
	_ = l.cfg.save()
}

// signedOff shows the sign-on screen again.
func (l *loginModel) signedOff(reason string) {
	l.busy = false
	l.status = reason
	if l.cursor >= 0 && l.cursor < len(l.cfg.Accounts) {
		l.selectAccount(l.cursor)
	}
	l.setFocus(focusPassword)
}

func (l *loginModel) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case loginFailedMsg:
		l.busy = false
		l.status = "Sign on failed: " + msg.err.Error()
		return nil
	case tea.KeyMsg:
		if l.busy {
			return nil
		}
		switch msg.String() {
		case "esc":
			return tea.Quit
		case "tab":
			l.moveFocus(1)
			return nil
		case "shift+tab":
			l.moveFocus(-1)
			return nil
		case "enter":
			if l.focus == focusAccounts && l.cursor >= 0 {
				l.selectAccount(l.cursor)
				if l.value(focusPassword) == "" {
					l.setFocus(focusPassword)
					return nil
				}
			}
			return l.signOn()
		}

		switch l.focus {
		case focusAccounts:
			switch msg.String() {
			case "up", "k":
				if l.cursor > 0 {
					l.cursor--
					l.selectAccount(l.cursor)
				}
			case "down", "j":
				if l.cursor < len(l.cfg.Accounts)-1 {
					l.cursor++
					l.selectAccount(l.cursor)
				}
			case "ctrl+d", "delete":
				l.cfg.remove(l.cursor)
				if err := l.cfg.save(); err != nil {
					l.status = "Unable to save config: " + err.Error()
				}
				if l.cursor >= len(l.cfg.Accounts) {
					l.cursor = len(l.cfg.Accounts) - 1
				}
				if l.cursor >= 0 {
					l.selectAccount(l.cursor)
				} else {
					l.setFocus(focusServer)
				}
			}
			return nil
		case focusSavePassword, focusSSL:
			switch msg.String() {
			case " ", "x":
				if l.focus == focusSavePassword {
					l.savePassword = !l.savePassword
				} else {
					l.ssl = !l.ssl
				}
			case "up":
				l.moveFocus(-1)
			case "down":
				if l.focus == focusSavePassword {
					l.moveFocus(1)
				}
			}
			return nil
		default:
			switch msg.String() {
			case "up":
				l.moveFocus(-1)
				return nil
			case "down":
				l.moveFocus(1)
				return nil
			}
			var cmd tea.Cmd
			l.inputs[l.focus], cmd = l.inputs[l.focus].Update(msg)
			return cmd
		}
	}

	if l.focus >= focusServer && l.focus <= focusPassword {
		var cmd tea.Cmd
		l.inputs[l.focus], cmd = l.inputs[l.focus].Update(msg)
		return cmd
	}
	return nil
}

// formStyle is how much the sign-on form is decorated.
type formStyle struct {
	// padY and padX pad the inside of the box.
	padY, padX int
	// gaps puts blank lines between the parts of the form.
	gaps bool
	// underline underlines the text fields.
	underline bool
	// allAccounts lists every saved screen name, instead of only the
	// selected one.
	allAccounts bool
}

// formStyles are the ways to draw the sign-on form in a box, from the most
// decorated to the least. The form is drawn the first way that fits the
// terminal, or without the box if none does.
var formStyles = []formStyle{
	{padY: 1, padX: 2, gaps: true, underline: true, allAccounts: true},
	{padY: 0, padX: 2, gaps: true, underline: true, allAccounts: true},
	{padY: 0, padX: 2, gaps: true, allAccounts: true},
	{padY: 0, padX: 1, allAccounts: true},
	{padY: 0, padX: 1},
}

func (l *loginModel) view(width, height int) string {
	for _, st := range formStyles {
		if box, ok := l.boxView(width, height, st); ok {
			return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
		}
	}
	return l.compactView(width, height)
}

// boxView renders the sign-on form in a box drawn in style st, if the box
// fits.
func (l *loginModel) boxView(width, height int, st formStyle) (string, bool) {
	boxWidth := min(72, width-2)
	inner := boxWidth - 2*st.padX
	labels := map[int]string{
		focusServer:     "Server",
		focusScreenName: "Screen name",
		focusPassword:   "Password",
	}
	labelWidth := 12
	if inner-labelWidth-2 < 20 {
		labels[focusScreenName] = "Name"
		labelWidth = 9
	}
	inputWidth := min(32, inner-labelWidth-2)
	if inputWidth < 10 {
		return "", false
	}

	var lines []string
	gap := func() {
		if st.gaps {
			lines = append(lines, "")
		}
	}
	label := func(text string, focused bool) string {
		text = fmt.Sprintf("%-*s", labelWidth, text)
		if focused {
			return focusedLabelStyle.Render(text)
		}
		return labelStyle.Render(text)
	}

	lines = append(lines, titleStyle.Render(fit(inner, "Go OSCAR — Sign On", "Sign On")))
	gap()

	if len(l.cfg.Accounts) > 0 && st.allAccounts {
		lines = append(lines, label("Saved screen names", l.focus == focusAccounts))
		for i, a := range l.cfg.Accounts {
			line := fmt.Sprintf("%s @ %s", a.ScreenName, a.Server)
			if !a.SSL {
				line += dimStyle.Render("  (no SSL)")
			}
			if a.Password != "" {
				line += dimStyle.Render("  (password saved)")
			}
			marker := "  "
			if i == l.cursor && l.focus == focusAccounts {
				marker = "> "
				line = selectedStyle.Render(line)
			}
			lines = append(lines, truncate(marker+line, inner))
		}
		gap()
	} else if len(l.cfg.Accounts) > 0 && l.cursor >= 0 {
		// only the selected screen name, which ↑/↓ changes
		a := l.cfg.Accounts[l.cursor]
		count := dimStyle.Render(fmt.Sprintf(" %d/%d", l.cursor+1, len(l.cfg.Accounts)))
		line := truncate(fmt.Sprintf("%s @ %s", a.ScreenName, a.Server), inner-labelWidth-1-lipgloss.Width(count))
		if l.focus == focusAccounts {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, truncate(label("Saved", l.focus == focusAccounts)+" "+line+count, inner))
	}

	for f := focusServer; f <= focusPassword; f++ {
		l.inputs[f].Width = inputWidth
		input := l.inputs[f].View()
		if st.underline {
			input = inputBoxStyle.Render(input)
		}
		lines = append(lines, label(labels[f], l.focus == f)+" "+input)
	}

	gap()
	lines = append(lines, l.checkbox(focusSavePassword, l.savePassword, "Save password"))
	lines = append(lines, l.checkbox(focusSSL, l.ssl, "Require SSL"))
	gap()

	if l.status != "" {
		lines = append(lines, lipgloss.NewStyle().Width(inner).Render(l.statusView()))
		gap()
	}

	help := "Enter: sign on • Tab: next field • Space: toggle • Esc: quit"
	if len(l.cfg.Accounts) > 0 {
		help += " • Del: forget saved screen name"
	}
	if st.padY > 0 {
		// wrapped by the box
		lines = append(lines, dimStyle.Render(help))
	} else {
		lines = append(lines, dimStyle.Render(fit(inner,
			help,
			"Enter: sign on • Tab: next field • Esc: quit",
			"Enter: sign on • Tab • Esc",
		)))
	}

	box := boxStyle.Padding(st.padY, st.padX).Width(boxWidth).Render(strings.Join(lines, "\n"))
	return box, lipgloss.Height(box) <= height
}

// compactView renders the sign-on form without the box, abbreviated to the
// terminal width and scrolled to the focused field, for terminals too small
// for the box.
func (l *loginModel) compactView(width, height int) string {
	var lines []string
	focusLine := 0

	lines = append(lines, titleStyle.Render(fit(width, "Go OSCAR — Sign On", "Sign On")))

	if len(l.cfg.Accounts) > 0 {
		label := fit(width, "Saved screen names", "Saved")
		if l.focus == focusAccounts {
			focusLine = len(lines)
			label = focusedLabelStyle.Render(label)
		}
		lines = append(lines, label)
		for i, a := range l.cfg.Accounts {
			marker, line := "  ", truncate(fmt.Sprintf("%s @ %s", a.ScreenName, a.Server), width-2)
			if i == l.cursor && l.focus == focusAccounts {
				focusLine = len(lines)
				marker, line = "> ", selectedStyle.Render(line)
			}
			lines = append(lines, truncate(marker+line, width))
		}
	}

	labels := map[int][2]string{
		focusServer:     {"Server", "Host"},
		focusScreenName: {"Screen name", "Name"},
		focusPassword:   {"Password", "Pass"},
	}
	for f := focusServer; f <= focusPassword; f++ {
		label := fmt.Sprintf("%-12s", labels[f][0])
		if width < 40 {
			label = fmt.Sprintf("%-5s", labels[f][1])
		}
		if l.focus == f {
			focusLine = len(lines)
			label = focusedLabelStyle.Render(label)
		}
		l.inputs[f].Width = max(1, width-lipgloss.Width(label)-1)
		lines = append(lines, truncate(label+l.inputs[f].View(), width))
	}

	if l.focus == focusSavePassword {
		focusLine = len(lines)
	}
	lines = append(lines, truncate(l.checkbox(focusSavePassword, l.savePassword, fit(width-4, "Save password", "Save")), width))
	if l.focus == focusSSL {
		focusLine = len(lines)
	}
	lines = append(lines, truncate(l.checkbox(focusSSL, l.ssl, fit(width-4, "Require SSL", "SSL")), width))
	lines = append(lines, dimStyle.Render(fit(width,
		"Enter: sign on • Tab: next field • Space: toggle • Esc: quit",
		"Enter: sign on • Tab • Esc",
		"Enter • Tab • Esc",
	)))

	// the status, such as why signing on failed, stays in view below the
	// focused field
	var status []string
	if l.status != "" {
		status = strings.Split(lipgloss.NewStyle().Width(width).Render(l.statusView()), "\n")
		// but leaves room for the form
		status = status[:min(len(status), max(1, height/2), max(0, height-1))]
	}
	if room := height - len(status); len(lines) > room {
		start := min(max(0, focusLine-room/2), len(lines)-room)
		lines = lines[start : start+room]
	}
	return strings.Join(append(lines, status...), "\n")
}

func (l *loginModel) statusView() string {
	style := statusStyle
	if strings.HasPrefix(l.status, "Sign on failed") || strings.HasPrefix(l.status, "Disconnected") {
		style = errorStyle
	}
	return style.Render(l.status)
}

// checkbox renders the checkbox for field f.
func (l *loginModel) checkbox(f int, checked bool, label string) string {
	check := "[ ]"
	if checked {
		check = "[x]"
	}
	line := check + " " + label
	if l.focus == f {
		line = focusedLabelStyle.Render(line)
	}
	return line
}
