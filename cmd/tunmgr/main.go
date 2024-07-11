package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/antoniomika/syncmap"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
	"golang.org/x/exp/maps"
)

type TunHandler struct {
	Listener   net.Listener
	RemoteAddr string
	LocalAddr  string
	Done       chan struct{}
}

type TunMgr struct {
	DockerClient *client.Client
	SSHClient    *ssh.Client
	Tunnels      *syncmap.Map[string, *syncmap.Map[string, *TunHandler]]
}

func (m *TunMgr) RemoveTunnels(containerID string) error {
	tunHandlers, ok := m.Tunnels.Load(containerID)
	if !ok {
		return fmt.Errorf("unable to find container: %s", containerID)
	}

	toDelete := []string{}

	logger := slog.With(
		slog.String("container_id", containerID),
	)

	tunHandlers.Range(func(rAddr string, handler *TunHandler) bool {
		innerLogger := logger.With(
			slog.String("remote_addr", rAddr),
		)

		innerLogger.Info(
			"Closing tunnel",
		)
		close(handler.Done)

		toDelete = append(toDelete, rAddr)

		remoteHost, remotePort, err := net.SplitHostPort(rAddr)
		if err != nil {
			innerLogger.Error(
				"Unable to parse remote addr",
				slog.Any("error", err),
			)
			return false
		}

		remotePortInt, err := strconv.Atoi(remotePort)
		if err != nil {
			innerLogger.Error(
				"Unable to parse remote port",
				slog.Any("error", err),
			)
			return false
		}

		forwardMessage := channelForwardMsg{
			addr:  remoteHost,
			rport: uint32(remotePortInt),
		}

		ok, _, err := m.SSHClient.SendRequest("cancel-tcpip-forward", true, ssh.Marshal(&forwardMessage))
		if err != nil {
			innerLogger.Error(
				"Error sending cancel request",
				slog.Any("error", err),
			)
			return false
		}

		if !ok {
			innerLogger.Error(
				"Request to cancel rejected by peer",
				slog.Any("error", err),
			)
		}

		return true
	})

	for _, rAddr := range toDelete {
		tunHandlers.Delete(rAddr)
	}

	m.Tunnels.Delete(containerID)

	return nil
}

type channelForwardMsg struct {
	addr  string
	rport uint32
}

type forwardedTCPPayload struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

func (m *TunMgr) AddTunnel(containerID string, remoteAddr string, localAddr string) (string, error) {
	tunHandlers, _ := m.Tunnels.LoadOrStore(containerID, syncmap.New[string, *TunHandler]())

	remoteHost, remotePort, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return "", err
	}

	remotePortInt, err := strconv.Atoi(remotePort)
	if err != nil {
		return "", err
	}

	forwardMessage := channelForwardMsg{
		addr:  remoteHost,
		rport: uint32(remotePortInt),
	}

	ok, resp, err := m.SSHClient.SendRequest("tcpip-forward", true, ssh.Marshal(&forwardMessage))
	if err != nil {
		return "", err
	}

	if !ok {
		return "", errors.New("ssh: tcpip-forward request denied by peer")
	}

	// If the original port was 0, then the remote side will
	// supply a real port number in the response.
	if remotePortInt == 0 {
		var p struct {
			Port uint32
		}
		if err := ssh.Unmarshal(resp, &p); err != nil {
			return "", err
		}
		remotePortInt = int(p.Port)
	}

	remoteAddr = fmt.Sprintf("%s:%d", remoteHost, remotePortInt)

	handler := &TunHandler{
		RemoteAddr: remoteAddr,
		LocalAddr:  localAddr,
		Done:       make(chan struct{}),
	}

	tunHandlers.Store(remoteAddr, handler)

	return remoteAddr, err
}

func (m *TunMgr) HandleChannels() {
	for ch := range m.SSHClient.HandleChannelOpen("forwarded-tcpip") {
		logger := slog.With(
			slog.String("channel_type", ch.ChannelType()),
			slog.String("extra_data", string(ch.ExtraData())),
		)

		logger.Info("Received channel open")

		switch channelType := ch.ChannelType(); channelType {
		case "forwarded-tcpip":
			var payload forwardedTCPPayload
			if err := ssh.Unmarshal(ch.ExtraData(), &payload); err != nil {
				logger.Error(
					"Unable to parse forwarded-tcpip payload",
					slog.Any("error", err),
				)
				ch.Reject(ssh.ConnectionFailed, "could not parse forwarded-tcpip payload: "+err.Error())
				continue
			}

			remoteAddr := fmt.Sprintf("%s:%d", payload.Addr, payload.Port)

			failed := false

			logger.Debug("About to iterate")

			m.Tunnels.Range(func(containerID string, tunHandlers *syncmap.Map[string, *TunHandler]) bool {
				tunLogger := logger.With(
					slog.String("container_id", containerID),
					slog.String("remote_addr", remoteAddr),
				)

				tunLogger.Debug("run iteration")

				handler, ok := tunHandlers.Load(remoteAddr)
				tunLogger.Debug("handler", slog.Any("handler", handler), slog.Bool("ok", ok))
				if !ok {
					tunLogger.Info("unable to find handler")

					failed = true
					return false
				}

				handlerLogger := tunLogger.With(
					slog.String("local_addr", handler.LocalAddr),
				)

				handlerLogger.Debug("About to start goroutine to accept")

				go func(ch ssh.NewChannel) {
					remoteConn, reqs, acceptErr := ch.Accept()
					if acceptErr != nil {
						handlerLogger.Error(
							"Error accepting connection from listener",
							slog.Any("error", acceptErr),
						)
						return
					}

					go ssh.DiscardRequests(reqs)

					go func() {
						defer remoteConn.Close()

						localConn, localErr := net.Dial("tcp", handler.LocalAddr)
						if localErr != nil {
							handlerLogger.Error(
								"Error starting local conn",
								slog.String("local_addr", handler.LocalAddr),
								slog.Any("error", localErr),
							)
							return
						}

						defer localConn.Close()

						wg := &sync.WaitGroup{}
						wg.Add(2)

						go func() {
							handlerLogger.Debug("Start copy to remote")
							defer wg.Done()
							n, err := io.Copy(remoteConn, localConn)
							handlerLogger.Debug(
								"Copy to remote conn",
								slog.Int64("n", n),
								slog.Any("error", err),
							)
							remoteConn.CloseWrite()
						}()

						go func() {
							handlerLogger.Debug("Start copy to local")
							defer wg.Done()
							n, err := io.Copy(localConn, remoteConn)
							handlerLogger.Debug(
								"Copy to local conn",
								slog.Int64("n", n),
								slog.Any("error", err),
							)
							if cw, ok := localConn.(interface{ CloseWrite() error }); ok {
								cw.CloseWrite()
							}
						}()

						wg.Wait()
					}()
				}(ch)

				return !ok
			})

			if failed {
				ch.Reject(ssh.ConnectionFailed, "unable to find tunnel")
			}
		}
	}
}

func NewTunMgr(dockerClient *client.Client, sshClient *ssh.Client) *TunMgr {
	return &TunMgr{
		DockerClient: dockerClient,
		SSHClient:    sshClient,
		Tunnels:      syncmap.New[string, *syncmap.Map[string, *TunHandler]](),
	}
}

func createDockerClient() *client.Client {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		slog.Error(
			"Unable to create docker client",
			slog.Any("error", err),
		)
		panic(err)
	}

	return dockerClient
}

func createSSHClient() *ssh.Client {
	rawConn, err := net.Dial("tcp", os.Getenv("REMOTE_HOST"))
	if err != nil {
		slog.Error(
			"Unable to create ssh client, tcp connection not established",
			slog.Any("error", err),
		)
		panic(err)
	}

	f, err := os.Open(os.Getenv("KEY_LOCATION"))
	if err != nil {
		slog.Error(
			"Unable to create ssh client, unable to open key",
			slog.Any("error", err),
		)
		panic(err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		slog.Error(
			"Unable to create ssh client, unable to read key",
			slog.Any("error", err),
		)
		panic(err)
	}

	var signer ssh.Signer

	if os.Getenv("KEY_PASSPHRASE") != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(os.Getenv("KEY_PASSPHRASE")))
	} else {
		signer, err = ssh.ParsePrivateKey(data)
	}

	if err != nil {
		slog.Error(
			"Unable to create ssh client, unable to parse key",
			slog.Any("error", err),
		)
		panic(err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, os.Getenv("REMOTE_HOSTNAME"), &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		User:            os.Getenv("REMOTE_USER"),
	})
	if err != nil {
		slog.Error(
			"Unable to create ssh client, unable to create client conn",
			slog.Any("error", err),
		)
		panic(err)
	}

	sshClient := ssh.NewClient(sshConn, chans, reqs)

	return sshClient
}

func handleContainerStart(tunMgr *TunMgr, logger *slog.Logger, containerID string, networks []string) error {
	containerInfo, err := tunMgr.DockerClient.ContainerInspect(context.Background(), containerID)
	if err != nil {
		logger.Error(
			"Unable to inspect container info for tunnel",
			slog.Any("error", err),
		)
		return err
	}

	logger.Debug(
		"Container info",
		slog.Any("container_info", containerInfo),
	)

	dnsNames := []string{containerInfo.ID[0:12], strings.TrimPrefix(containerInfo.Name, "/")}

	logger.Debug(
		"DNS Names",
		slog.Any("dns_names", dnsNames),
	)

	logger.Debug(
		"Networks",
		slog.Any("networks", networks),
		slog.Any("container_networks", maps.Keys(containerInfo.NetworkSettings.Networks)),
	)

	logger.Debug(
		"Exposed Ports",
		slog.Any("exposed_ports", containerInfo.Config.ExposedPorts),
	)

	exposedPorts := []int{}
	for port := range containerInfo.Config.ExposedPorts {
		exposedPorts = append(exposedPorts, port.Int())
	}

	slices.Sort(dnsNames)
	exposedPorts = slices.Compact(exposedPorts)

	for _, netw := range maps.Keys(containerInfo.NetworkSettings.Networks) {
		if slices.Contains(networks, strings.ToLower(strings.TrimSpace(netw))) {
			dnsNames = append(dnsNames, containerInfo.NetworkSettings.Networks[netw].DNSNames...)
			slices.Sort(dnsNames)
			dnsNames = slices.Compact(dnsNames)

			for _, port := range exposedPorts {
				for _, dnsName := range dnsNames {
					tunnelRemote := fmt.Sprintf("%s:%d", dnsName, port)
					tunnelLocal := fmt.Sprintf("%s:%d", containerInfo.NetworkSettings.Networks[netw].IPAddress, port)

					logger.Info(
						"Adding tunnel",
						slog.String("remote", tunnelRemote),
						slog.String("local", tunnelLocal),
					)

					remoteAddr, err := tunMgr.AddTunnel(containerInfo.ID, tunnelRemote, tunnelLocal)
					if err != nil {
						logger.Error(
							"Unable to start tunnel",
							slog.String("remote", tunnelRemote),
							slog.String("local", tunnelLocal),
							slog.Any("error", err),
						)
					}

					logger.Debug(
						"Remote addr",
						slog.String("remote_addr", remoteAddr),
					)
				}
			}
		}
	}

	return nil
}

func main() {
	var rootLoggerLevel slog.Level
	logLevel := os.Getenv("LOG_LEVEL")

	switch strings.ToLower(logLevel) {
	case "debug":
		rootLoggerLevel = slog.LevelDebug
	case "warn":
		rootLoggerLevel = slog.LevelWarn
	case "error":
		rootLoggerLevel = slog.LevelError
	default:
		rootLoggerLevel = slog.LevelInfo
	}

	rootLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: rootLoggerLevel,
	}))

	slog.SetDefault(rootLogger)

	dockerClient := createDockerClient()
	defer dockerClient.Close()

	networks := []string{}
	networksToCheck := strings.TrimSpace(os.Getenv("NETWORKS"))
	if networksToCheck == "" {
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			rootLogger.Error(
				"Unable to get hostname",
				slog.Any("error", err),
			)
		} else {
			info, err := dockerClient.ContainerInspect(context.Background(), hostname)
			if err != nil {
				rootLogger.Error(
					"Unable to find networks. Please provide a list to monitor",
					slog.Any("error", err),
				)
				panic(err)
			}

			for _, netw := range maps.Keys(info.NetworkSettings.Networks) {
				networks = append(networks, strings.ToLower(strings.TrimSpace(netw)))
			}
		}
	} else {
		for _, netw := range strings.Split(networksToCheck, ",") {
			networks = append(networks, strings.ToLower(strings.TrimSpace(netw)))
		}
	}

	sshClient := createSSHClient()
	defer sshClient.Close()

	rootLogger.Info(
		"Started tunmgr",
		slog.String("docker", dockerClient.DaemonHost()),
		slog.String("docker_version", dockerClient.ClientVersion()),
		slog.String("ssh", sshClient.RemoteAddr().String()),
		slog.String("ssh_user", sshClient.User()),
	)

	eventCtx, cancelEventCtx := context.WithCancel(context.Background())

	clientEvents, errs := dockerClient.Events(eventCtx, events.ListOptions{})

	tunMgr := NewTunMgr(dockerClient, sshClient)

	go tunMgr.HandleChannels()
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})

		err := http.ListenAndServe("localhost:8080", nil)
		if err != nil {
			rootLogger.Error("error with http server", slog.Any("error", err))
		}
	}()

	containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{})
	if err != nil {
		rootLogger.Error(
			"unable to list container from docker",
			slog.Any("error", err),
		)
	}

	for _, container := range containers {
		err := handleContainerStart(tunMgr, rootLogger, container.ID, networks)
		if err != nil {
			rootLogger.Error(
				"Unable to add tunnels for container",
				slog.String("container_id", container.ID),
				slog.Any("error", err),
				slog.Any("container_data", container),
			)
			break
		}
	}

	for {
		select {
		case event := <-clientEvents:
			switch event.Type {
			case events.ContainerEventType:
				logger := slog.With(
					slog.String("event", string(event.Action)),
					slog.String("container_id", event.Actor.ID),
				)
				switch event.Action {
				case events.ActionStart:
					logger.Info("Received start")
					err := handleContainerStart(tunMgr, logger, event.Actor.ID, networks)
					if err != nil {
						logger.Error(
							"Unable to add tunnels for container",
							slog.Any("error", err),
						)
						break
					}
				case events.ActionDie:
					logger.Info("Received die")
					err := tunMgr.RemoveTunnels(event.Actor.ID)
					if err != nil {
						logger.Error(
							"Unable to remove tunnels for container",
							slog.Any("error", err),
						)
						break
					}
				default:
					logger.Debug(
						"Unhandled container action",
						slog.Any("event_data", event),
					)
				}
			default:
				slog.Debug(
					"Unhandled daemon event",
					slog.Any("event_data", event),
				)
			}
		case err := <-errs:
			cancelEventCtx()
			slog.Error(
				"Error receiving events from daemon",
				slog.Any("error", err),
			)
			panic(err)
		}
	}
}
