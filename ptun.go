package ptun

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
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

type TcpIpForwardFn = func(*ssh.Server, *gossh.ServerConn, gossh.NewChannel, ssh.Context)
type HttpHandlerFn = func(ctx ssh.Context) http.Handler

type ctxRemoteConnectionsKey struct{}

func getRemoteConnectionsCtx(ctx ssh.Context) (map[uint32]ssh.PublicKey, error) {
	payload, ok := ctx.Value(ctxAddressKey{}).(map[uint32]ssh.PublicKey)
	if payload == nil || !ok {
		return payload, fmt.Errorf("address not set on `ssh.Context()` for connection")
	}
	return payload, nil
}
func setConnectionsCtx(ctx ssh.Context, address string) {
	ctx.SetValue(ctxAddressKey{}, address)
}

type WebTunnel interface {
	GetHttpHandler() HttpHandlerFn
	CreateListener(ctx ssh.Context) (net.Listener, error)
	CreateConn(ctx ssh.Context) (net.Conn, error)
	GetLogger() *slog.Logger
}

type remoteForwardRequest struct {
	BindAddr string
	BindPort uint32
}

type remoteForwardSuccess struct {
	BindPort uint32
}

type remoteForwardCancelRequest struct {
	BindAddr string
	BindPort uint32
}

type remoteForwardChannelData struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

type RemoteForwards struct {
	Listener net.Listener
	Pubkey   ssh.PublicKey
}

// ForwardedTCPHandler can be enabled by creating a ForwardedTCPHandler and
// adding the HandleSSHRequest callback to the server's RequestHandlers under
// tcpip-forward and cancel-tcpip-forward.
type ForwardedTCPHandler struct {
	Forwards map[string]RemoteForwards
	sync.Mutex
}

var forwardedTCPChannelType = "forwarded-tcpip"

func (h *ForwardedTCPHandler) GetListenersByPubkey(pubkey ssh.PublicKey) []net.Listener {
	list := []net.Listener{}
	for _, v := range h.Forwards {
		if bytes.Equal(v.Pubkey.Marshal(), pubkey.Marshal()) {
			list = append(list, v.Listener)
		}
	}
	return list
}

func (h *ForwardedTCPHandler) HandleSSHRequest(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	h.Lock()
	if h.Forwards == nil {
		h.Forwards = make(map[string]RemoteForwards)
	}
	h.Unlock()
	conn := ctx.Value(ssh.ContextKeyConn).(*gossh.ServerConn)
	switch req.Type {
	case "tcpip-forward":
		var reqPayload remoteForwardRequest
		if err := gossh.Unmarshal(req.Payload, &reqPayload); err != nil {
			// TODO: log parse failure
			return false, []byte{}
		}
		addr := net.JoinHostPort(reqPayload.BindAddr, strconv.Itoa(int(reqPayload.BindPort)))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			// TODO: log listen failure
			return false, []byte{}
		}
		_, destPortStr, _ := net.SplitHostPort(ln.Addr().String())
		destPort, _ := strconv.Atoi(destPortStr)
		pubkey, _ := ctx.Value(ssh.ContextKeyPublicKey).(ssh.PublicKey)
		remoteForward := RemoteForwards{
			Listener: ln,
			Pubkey:   pubkey,
		}
		fmt.Printf("%+v\n", remoteForward)
		h.Lock()
		h.Forwards[addr] = remoteForward
		h.Unlock()
		go func() {
			<-ctx.Done()
			h.Lock()
			rf, ok := h.Forwards[addr]
			h.Unlock()
			if ok {
				rf.Listener.Close()
			}
		}()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					// TODO: log accept failure
					break
				}
				originAddr, orignPortStr, _ := net.SplitHostPort(c.RemoteAddr().String())
				originPort, _ := strconv.Atoi(orignPortStr)
				payload := gossh.Marshal(&remoteForwardChannelData{
					DestAddr:   reqPayload.BindAddr,
					DestPort:   uint32(destPort),
					OriginAddr: originAddr,
					OriginPort: uint32(originPort),
				})
				go func() {
					ch, reqs, err := conn.OpenChannel(forwardedTCPChannelType, payload)
					if err != nil {
						c.Close()
						return
					}
					go gossh.DiscardRequests(reqs)
					go func() {
						defer ch.Close()
						defer c.Close()
						io.Copy(ch, c)
					}()
					go func() {
						defer ch.Close()
						defer c.Close()
						io.Copy(c, ch)
					}()
				}()
			}
			h.Lock()
			delete(h.Forwards, addr)
			h.Unlock()
		}()
		return true, gossh.Marshal(&remoteForwardSuccess{uint32(destPort)})

	case "cancel-tcpip-forward":
		var reqPayload remoteForwardCancelRequest
		if err := gossh.Unmarshal(req.Payload, &reqPayload); err != nil {
			// TODO: log parse failure
			return false, []byte{}
		}
		addr := net.JoinHostPort(reqPayload.BindAddr, strconv.Itoa(int(reqPayload.BindPort)))
		h.Lock()
		rf, ok := h.Forwards[addr]
		h.Unlock()
		if ok {
			rf.Listener.Close()
		}
		return true, nil
	default:
		return false, nil
	}
}

func WithRemoteForward(handler *ForwardedTCPHandler) ssh.Option {
	return func(serv *ssh.Server) error {
		serv.RequestHandlers = map[string]ssh.RequestHandler{
			"tcpip-forward":        handler.HandleSSHRequest,
			"cancel-tcpip-forward": handler.HandleSSHRequest,
		}
		return nil
	}
}

func WithWebTunnel(handler WebTunnel) ssh.Option {
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
			log.Error("unable to serve http content", "err", err)
		}
	}()

	return listener, nil
}

func localForwardHandler(handler WebTunnel) TcpIpForwardFn {
	return func(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
		logger := handler.GetLogger()
		check := forwardedTCPPayload{}
		err := gossh.Unmarshal(newChan.ExtraData(), &check)
		if err != nil {
			msg := "error parsing forward data"
			logger.Error(msg, "err", err)
			newChan.Reject(gossh.ConnectionFailed, msg+": "+err.Error())
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

		listener, err := httpServe(handler, ctx, log)
		if err != nil {
			log.Info("unable to create listener", "err", err)
			return
		}
		defer listener.Close()

		go func() {
			downConn, err := handler.CreateConn(ctx)
			if err != nil {
				log.Error("unable to connect to unix socket", "err", err)
				return
			}
			defer downConn.Close()

			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				defer ch.CloseWrite()
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
	}
}
