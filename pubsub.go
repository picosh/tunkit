package ptun

import (
	"log/slog"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type PubSub interface {
	GetLogger() *slog.Logger
	GetForwards() []*RemoteForwards
	HandleRequest(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte)
}

func WithPubSub(handler PubSub) ssh.Option {
	return func(serv *ssh.Server) error {
		serv.RequestHandlers = map[string]ssh.RequestHandler{
			"tcpip-forward":        handler.HandleRequest,
			"cancel-tcpip-forward": handler.HandleRequest,
		}
		return nil
	}
}
