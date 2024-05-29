package ptun

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type forwardedTCPPayload struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

type LocalForwardFn = func(*ssh.Server, *gossh.ServerConn, gossh.NewChannel, ssh.Context)

type Tunnel interface {
	CreateConn(ctx ssh.Context) (net.Conn, error)
	GetLogger() *slog.Logger
	Close(ctx ssh.Context) error
}

func WithTunnel(handler Tunnel) ssh.Option {
	return func(serv *ssh.Server) error {
		if serv.ChannelHandlers == nil {
			serv.ChannelHandlers = map[string]ssh.ChannelHandler{
				"session": ssh.DefaultSessionHandler,
			}
		}
		serv.ChannelHandlers["direct-tcpip"] = localForwardHandler(handler)
		return nil
	}
}

func localForwardHandler(handler Tunnel) LocalForwardFn {
	return func(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
		check := &forwardedTCPPayload{}
		err := gossh.Unmarshal(newChan.ExtraData(), check)
		logger := handler.GetLogger()
		if err != nil {
			logger.Error(
				"error unmarshaling information",
				"err", err,
			)
			return
		}

		log := logger.With(
			"addr", check.Addr,
			"port", check.Port,
			"origAddr", check.OriginAddr,
			"origPort", check.OriginPort,
		)
		log.Info("local forward request")

		ch, reqs, err := newChan.Accept()
		if err != nil {
			log.Error("cannot accept new channel", "err", err)
			return
		}
		go gossh.DiscardRequests(reqs)

		go func() {
			downConn, err := handler.CreateConn(ctx)
			if err != nil {
				log.Error("unable to connect to conn", "err", err)
				ch.Close()
				return
			}
			defer downConn.Close()

			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				defer func() {
					_ = ch.CloseWrite()
				}()
				defer downConn.Close()
				_, err := io.Copy(ch, downConn)
				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						log.Error("io copy", "err", err)
					}
				}
			}()
			go func() {
				defer wg.Done()
				defer ch.Close()
				defer downConn.Close()
				_, err := io.Copy(downConn, ch)
				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						log.Error("io copy", "err", err)
					}
				}
			}()

			wg.Wait()
		}()

		err = conn.Wait()
		if err != nil {
			log.Error("conn wait error", "err", err)
		}
		err = handler.Close(ctx)
		if err != nil {
			log.Error("tunnel handler error", "err", err)
		}
	}
}
