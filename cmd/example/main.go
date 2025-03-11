package main

import (
	"fmt"
	"net/http"

	"github.com/picosh/pico/pssh"
	"golang.org/x/crypto/ssh"
)

func getPubkey(ctx *pssh.SSHServerConnSession) (ssh.PublicKey, error) {
	pubkey := ctx.PublicKey()
	if pubkey == nil {
		return pubkey, fmt.Errorf("pubkey not set on `*pssh.SSHServerConnSession()` for connection")
	}
	return pubkey, nil
}
func keyForSha256(pk ssh.PublicKey) string {
	return ssh.FingerprintSHA256(pk)
}

func authHandler(ctx *pssh.SSHServerConnSession, key ssh.PublicKey) bool {
	return true
}

func serveMux(ctx *pssh.SSHServerConnSession) http.Handler {
	clientName := ctx.User()
	pubkey, err := getPubkey(ctx)
	if err != nil {
		panic(err)
	}
	fingerprint := keyForSha256(pubkey)

	router := http.NewServeMux()
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(fmt.Sprintf("Hello, %s!\nYour pubkey: %s\n", clientName, fingerprint)))
		if err != nil {
			fmt.Println(err)
		}
	})

	return router
}

func main() {
	// host := os.Getenv("SSH_HOST")
	// if host == "" {
	// 	host = "0.0.0.0"
	// }
	// port := os.Getenv("SSH_PORT")
	// if port == "" {
	// 	port = "2222"
	// }

	// logger := slog.Default()
	// s, err := wish.NewServer(
	// 	wish.WithAddress(fmt.Sprintf("%s:%s", host, port)),
	// 	wish.WithHostKeyPath("ssh_data/term_info_ed25519"),
	// 	wish.WithPublicKeyAuth(authHandler),
	// 	tunkit.WithWebTunnel(tunkit.NewWebTunnelHandler(serveMux, logger)),
	// )

	// if err != nil {
	// 	logger.Error("could not create server", "err", err)
	// }

	// done := make(chan os.Signal, 1)
	// signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	// logger.Info("starting SSH server", "host", host, "port", port)
	// go func() {
	// 	if err = s.ListenAndServe(); err != nil {
	// 		logger.Error("serve error", "err", err)
	// 		os.Exit(1)
	// 	}
	// }()

	// <-done
	// logger.Info("stopping SSH server")
	// ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	// defer func() { cancel() }()
	// if err := s.Shutdown(ctx); err != nil {
	// 	logger.Error("shutdown", "err", err)
	// 	os.Exit(1)
	// }
}
