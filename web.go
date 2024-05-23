package ptun

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/charmbracelet/ssh"
)

type HttpHandlerFn = func(ctx ssh.Context) http.Handler

type ctxAddressKey struct{}

func getAddressCtx(ctx ssh.Context) (string, error) {
	address, ok := ctx.Value(ctxAddressKey{}).(string)
	if address == "" || !ok {
		return address, fmt.Errorf("address not set on `ssh.Context()` for connection")
	}
	return address, nil
}
func setAddressCtx(ctx ssh.Context, address string) {
	ctx.SetValue(ctxAddressKey{}, address)
}

type WebTunnelHandler struct {
	HttpHandler HttpHandlerFn
	Logger      *slog.Logger
}

func NewWebTunnelHandler(handler HttpHandlerFn, logger *slog.Logger) *WebTunnelHandler {
	return &WebTunnelHandler{
		HttpHandler: handler,
		Logger:      logger,
	}
}

func (wt *WebTunnelHandler) GetLogger() *slog.Logger {
	return wt.Logger
}

func (wt *WebTunnelHandler) CreateListener(ctx ssh.Context) (net.Listener, error) {
	tempFile, err := os.CreateTemp("", "")
	if err != nil {
		return nil, err
	}

	tempFile.Close()
	address := tempFile.Name()
	os.Remove(address)

	connListener, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	setAddressCtx(ctx, address)

	return connListener, nil
}

func (wt *WebTunnelHandler) CreateConn(ctx ssh.Context) (net.Conn, error) {
	address, err := getAddressCtx(ctx)
	if err != nil {
		return nil, err
	}

	return net.Dial("unix", address)
}

func (wt *WebTunnelHandler) Serve(listener net.Listener, ctx ssh.Context) error {
	return http.Serve(listener, wt.HttpHandler(ctx))
}
