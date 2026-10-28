// Command tui_client is an interactive terminal OSCAR client for testing Open
// OSCAR Server. It supports the buddy list, instant messages, chat rooms,
// profiles, away messages, blocking and warning.
//
// Usage:
//
//	go run ./cmd/tui_client [-server host:port] [-screenname name] [-password pass] [-ssl=false]
//
// When the server, screen name and password are all given, the client signs
// on right away. Otherwise it shows a sign-on screen, which also lists the
// screen names saved in the config file.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	var (
		server     = flag.String("server", "", "OSCAR server address (host:port)")
		screenName = flag.String("screenname", "", "screen name to sign on with")
		password   = flag.String("password", "", "password to sign on with")
		configPath = flag.String("config", defaultConfigPath(), "path of the config file that remembers screen names")
		plain      = flag.Bool("plain", false, "strip HTML formatting instead of rendering it with terminal colors")
		logPath    = flag.String("log", "", "write debug logs to this file")
		mouse      = flag.Bool("mouse", true, "make tabs and buddies clickable; most terminals then need Shift held to select text")
		ssl        = flag.Bool("ssl", true, "require SSL; -ssl=false signs on in plaintext")
		caFile     = flag.String("cafile", "", "PEM file of CA certificates to trust for SSL, in addition to the system roots")
		insecure   = flag.Bool("insecure", false, "skip verifying the server's SSL certificate")
	)
	flag.Parse()

	// -ssl overrides the saved account only if given
	var sslArg *bool
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "ssl" {
			sslArg = ssl
		}
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "unable to open log file: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to load config: %v\n", err)
		os.Exit(1)
	}

	tlsConfig, err := newTLSConfig(*caFile, *insecure)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to set up SSL: %v\n", err)
		os.Exit(1)
	}

	m := newModel(cfg, logger, *plain, loginArgs{
		server:     *server,
		screenName: *screenName,
		password:   *password,
		ssl:        sslArg,
		tlsConfig:  tlsConfig,
	})
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if *mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// newTLSConfig returns the TLS config for SSL connections. It trusts the CA
// certificates in caFile as well as the system roots, and skips verification
// altogether if insecure is set.
func newTLSConfig(caFile string, insecure bool) (*tls.Config, error) {
	cfg := &tls.Config{InsecureSkipVerify: insecure}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("no certificates found in " + caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
