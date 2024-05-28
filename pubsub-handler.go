package ptun

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/exp/maps"
)

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

// PubSubHandler can be enabled by creating a PubSubHandler and
// adding the HandleSSHRequest callback to the server's RequestHandlers under
// tcpip-forward and cancel-tcpip-forward.
type PubSubHandler struct {
	Logger *slog.Logger
	sync.Mutex
	forwards map[string]*RemoteForwards
}

func NewPubSubHandler(logger *slog.Logger) *PubSubHandler {
	return &PubSubHandler{
		Logger: logger,
	}
}

var forwardedTCPChannelType = "forwarded-tcpip"

func (h *PubSubHandler) GetForwards() []*RemoteForwards {
	return maps.Values(h.forwards)
}

func (h *PubSubHandler) GetLogger() *slog.Logger {
	return h.Logger
}

func (h *PubSubHandler) GetForwardsByPubkey(pubkey ssh.PublicKey) []net.Listener {
	list := []net.Listener{}
	for _, v := range h.forwards {
		if bytes.Equal(v.Pubkey.Marshal(), pubkey.Marshal()) {
			list = append(list, v.Listener)
		}
	}
	return list
}

func (h *PubSubHandler) HandleRequest(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	logger := h.GetLogger()
	h.Lock()
	if h.forwards == nil {
		h.forwards = make(map[string]*RemoteForwards)
	}
	h.Unlock()
	conn := ctx.Value(ssh.ContextKeyConn).(*gossh.ServerConn)
	switch req.Type {
	case "tcpip-forward":
		var reqPayload remoteForwardRequest
		if err := gossh.Unmarshal(req.Payload, &reqPayload); err != nil {
			logger.Error("failed to parse request payload", "err", err)
			return false, []byte{}
		}
		addr := net.JoinHostPort(reqPayload.BindAddr, strconv.Itoa(int(reqPayload.BindPort)))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			logger.Error("failed create net listener", "err", err)
			return false, []byte{}
		}
		_, destPortStr, _ := net.SplitHostPort(ln.Addr().String())
		destPort, _ := strconv.Atoi(destPortStr)
		pubkey, _ := ctx.Value(ssh.ContextKeyPublicKey).(ssh.PublicKey)
		remoteForward := RemoteForwards{
			Listener: ln,
			Pubkey:   pubkey,
		}
		h.Lock()
		h.forwards[addr] = &remoteForward
		h.Unlock()
		go func() {
			<-ctx.Done()
			h.Lock()
			rf, ok := h.forwards[addr]
			h.Unlock()
			if ok {
				rf.Listener.Close()
			}
		}()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					logger.Error("failed accept channel", "err", err)
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
			delete(h.forwards, addr)
			h.Unlock()
		}()
		return true, gossh.Marshal(&remoteForwardSuccess{uint32(destPort)})

	case "cancel-tcpip-forward":
		var reqPayload remoteForwardCancelRequest
		if err := gossh.Unmarshal(req.Payload, &reqPayload); err != nil {
			logger.Error("failed parse payload", "err", err)
			return false, []byte{}
		}
		addr := net.JoinHostPort(reqPayload.BindAddr, strconv.Itoa(int(reqPayload.BindPort)))
		h.Lock()
		rf, ok := h.forwards[addr]
		h.Unlock()
		if ok {
			rf.Listener.Close()
		}
		return true, nil
	default:
		return false, nil
	}
}
