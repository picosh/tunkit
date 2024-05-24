package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/picosh/ptun"
	gossh "golang.org/x/crypto/ssh"
)

type handler struct {
	logger *slog.Logger
}

func (h *handler) CreateConn(ctx ssh.Context) (net.Conn, error) {
	rawConn, err := net.Dial("tcp", os.Getenv("REMOTE_HOST"))
	if err != nil {
		return nil, err
	}

	f, err := os.Open(os.Getenv("KEY_LOCATION"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var signer gossh.Signer

	if os.Getenv("KEY_PASSPHRASE") != "" {
		signer, err = gossh.ParsePrivateKeyWithPassphrase(data, []byte(os.Getenv("KEY_PASSPHRASE")))
	} else {
		signer, err = gossh.ParsePrivateKey(data)
	}

	if err != nil {
		return nil, err
	}

	sshConn, chans, reqs, err := gossh.NewClientConn(rawConn, os.Getenv("REMOTE_HOSTNAME"), &gossh.ClientConfig{
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		User:            os.Getenv("REMOTE_USER"),
	})
	if err != nil {
		return nil, err
	}
	sshClient := gossh.NewClient(sshConn, chans, reqs)
	return sshClient.Dial(os.Getenv("REMOTE_PROTOCOL"), os.Getenv("REMOTE_ADDRESS"))
}

func (h *handler) GetLogger() *slog.Logger {
	return h.logger
}

var _ ptun.Tunnel = &handler{}

func main() {
	host := os.Getenv("SSH_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	port := os.Getenv("SSH_PORT")
	if port == "" {
		port = "2222"
	}
	keyPath := os.Getenv("SSH_AUTHORIZED_KEYS")
	if keyPath == "" {
		keyPath = "ssh_data/authorized_keys"
	}
	logger := slog.Default()

	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%s", host, port)),
		wish.WithHostKeyPath("ssh_data/term_info_ed25519"),
		wish.WithAuthorizedKeys(keyPath),
		ptun.WithTunnel(&handler{
			logger: logger,
		}),
	)

	if err != nil {
		logger.Error("could not create server", "err", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	logger.Info("starting SSH server", "host", host, "port", port)
	go func() {
		if err = s.ListenAndServe(); err != nil {
			logger.Error("serve error", "err", err)
			os.Exit(1)
		}
	}()

	<-done
	logger.Info("stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil {
		logger.Error("shutdown", "err", err)
		os.Exit(1)
	}
}
