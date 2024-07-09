package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/picosh/tunkit"
	gossh "golang.org/x/crypto/ssh"
)

func keyForSha256(pk ssh.PublicKey) string {
	return gossh.FingerprintSHA256(pk)
}

func CliMiddleware(handler tunkit.PubSub) wish.Middleware {
	log := handler.GetLogger()

	return func(next ssh.Handler) ssh.Handler {
		return func(sesh ssh.Session) {
			_, _, activePty := sesh.Pty()
			if activePty {
				next(sesh)
				return
			}

			args := sesh.Command()
			forwards := handler.GetForwards()

			cmd := strings.TrimSpace(args[0])
			if cmd == "emit" {
				msg := args[1]

				if len(forwards) == 0 {
					wish.Println(sesh, "no listeners")
					log.Info("no listeners")
					return
				}

				for _, rf := range forwards {
					addr := rf.Listener.Addr()
					furl := fmt.Sprintf(
						"http://%s?msg=%s",
						addr.String(),
						msg,
					)
					logger := log.With(
						"pubkey", keyForSha256(rf.Pubkey),
						"addr", addr,
						"msg", msg,
						"url", furl,
					)

					wish.Printf(sesh, "[GET] %s\n", furl)
					logger.Info("emitting to listener")

					_, err := http.Get(furl)
					if err != nil {
						logger.Error("unable send message", "err", err)
					}
				}
				return
			} else if cmd == "ls" {
				if len(forwards) == 0 {
					log.Info("no listeners")
					wish.Println(sesh, "no listeners")
					return
				}

				for _, rf := range forwards {
					addr := rf.Listener.Addr()
					pk := keyForSha256(rf.Pubkey)
					logger := log.With(
						"pubkey", pk,
						"addr", addr,
					)
					logger.Info("listener")
					wish.Println(sesh, fmt.Sprintf("addr:%s pubkey:%s", addr, pk))
				}
				return
			} else {
				next(sesh)
				return
			}
		}
	}
}
