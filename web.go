package ptun

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/charmbracelet/ssh"
)

type HttpHandlerFn = func(ctx ssh.Context) http.Handler

type WebTunnel interface {
	GetHttpHandler() HttpHandlerFn
	CreateListener(ctx ssh.Context) (net.Listener, error)
	CreateConn(ctx ssh.Context) (net.Conn, error)
	GetLogger() *slog.Logger
	Close(ctx ssh.Context) error
}

func WithWebTunnel(handler WebTunnel) ssh.Option {
	return WithTunnel(handler)
}

type ctxListenerKey struct{}

func getListenerCtx(ctx ssh.Context) (net.Listener, error) {
	listener, ok := ctx.Value(ctxListenerKey{}).(net.Listener)
	if listener == nil || !ok {
		return nil, fmt.Errorf("listener not set on `ssh.Context()` for connection")
	}
	return listener, nil
}
func setListenerCtx(ctx ssh.Context, listener net.Listener) {
	ctx.SetValue(ctxListenerKey{}, listener)
}

func httpServe(handler WebTunnel, ctx ssh.Context, log *slog.Logger) (net.Listener, error) {
	cached, _ := getListenerCtx(ctx)
	if cached != nil {
		return cached, nil
	}

	listener, err := handler.CreateListener(ctx)
	if err != nil {
		return nil, err
	}
	setListenerCtx(ctx, listener)

	go func() {
		httpHandler := handler.GetHttpHandler()
		err := http.Serve(listener, httpHandler(ctx))
		if err != nil {
			log.Error("serving http content", "err", err)
		}
	}()

	return listener, nil
}
