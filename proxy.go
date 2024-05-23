package ptun

import (
	"net"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type ProxyTunnelHandler struct {
	*WebTunnelHandler
}

func (tun *ProxyTunnelHandler) CreateConn(ctx ssh.Context) (net.Conn, error) {
	rawConn, err := net.Dial("tcp", "pico.sh:22")
	if err != nil {
		return nil, err
	}

	sshConn, chans, reqs, err := gossh.NewClientConn(rawConn, "pico.sh", &gossh.ClientConfig{})
	if err != nil {
		return nil, err
	}

	sshClient := gossh.NewClient(sshConn, chans, reqs)

	dbConn, err := sshClient.Dial("unix", "/var/run/port-forward/2c537858")
	if err != nil {
		return nil, err
	}

	return dbConn, nil
}

func (tun *ProxyTunnelHandler) Serve(listener net.Listener, ctx ssh.Context) error {
	return nil
}
