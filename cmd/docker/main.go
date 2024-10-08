package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/picosh/tunkit"
)

type ErrorHandler struct {
	Err error
}

func (e *ErrorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println(e.Err.Error())
	http.Error(w, e.Err.Error(), http.StatusInternalServerError)
}

func serveMux(ctx ssh.Context) http.Handler {
	router := http.NewServeMux()
	slug := ctx.User()

	registryUrl := os.Getenv("REGISTRY_URL")
	if registryUrl == "" {
		registryUrl = "registry:5000"
	}

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{
		Scheme: "http",
		Host:   registryUrl,
	})

	oldDirector := proxy.Director

	proxy.Director = func(r *http.Request) {
		log.Printf("%+v", r)
		oldDirector(r)

		if strings.HasSuffix(r.URL.Path, "_catalog") || r.URL.Path == "/v2" || r.URL.Path == "/v2/" {
			return
		}

		fullPath := strings.TrimPrefix(r.URL.Path, "/v2")

		newPath, err := url.JoinPath("/v2", slug, fullPath)
		if err != nil {
			return
		}

		r.URL.Path = newPath

		query := r.URL.Query()

		if query.Has("from") {
			joinedFrom, err := url.JoinPath(slug, query.Get("from"))
			if err != nil {
				return
			}
			query.Set("from", joinedFrom)

			r.URL.RawQuery = query.Encode()
		}

		log.Printf("%+v", r)
	}

	proxy.ModifyResponse = func(r *http.Response) error {
		log.Printf("%+v", r)

		if slug != "" && r.Request.Method == http.MethodGet && strings.HasSuffix(r.Request.URL.Path, "_catalog") {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return err
			}

			err = r.Body.Close()
			if err != nil {
				return err
			}

			var data map[string]any
			err = json.Unmarshal(b, &data)
			if err != nil {
				return err
			}

			var newRepos []string

			if repos, ok := data["repositories"].([]any); ok {
				for _, repo := range repos {
					if repoStr, ok := repo.(string); ok && strings.HasPrefix(repoStr, slug) {
						newRepos = append(newRepos, strings.Replace(repoStr, fmt.Sprintf("%s/", slug), "", 1))
					}
				}
			}

			data["repositories"] = newRepos

			newB, err := json.Marshal(data)
			if err != nil {
				return err
			}

			jsonBuf := bytes.NewBuffer(newB)

			r.ContentLength = int64(jsonBuf.Len())
			r.Header.Set("Content-Length", strconv.FormatInt(r.ContentLength, 10))
			r.Body = io.NopCloser(jsonBuf)
		}

		if slug != "" && r.Request.Method == http.MethodGet && (strings.Contains(r.Request.URL.Path, "/tags/") || strings.Contains(r.Request.URL.Path, "/manifests/")) {
			splitPath := strings.Split(r.Request.URL.Path, "/")

			if len(splitPath) > 1 {
				ele := splitPath[len(splitPath)-2]
				if ele == "tags" || ele == "manifests" {
					b, err := io.ReadAll(r.Body)
					if err != nil {
						return err
					}

					err = r.Body.Close()
					if err != nil {
						return err
					}

					var data map[string]any
					err = json.Unmarshal(b, &data)
					if err != nil {
						return err
					}

					if name, ok := data["name"].(string); ok {
						if strings.HasPrefix(name, slug) {
							data["name"] = strings.Replace(name, fmt.Sprintf("%s/", slug), "", 1)
						}
					}

					newB, err := json.Marshal(data)
					if err != nil {
						return err
					}

					jsonBuf := bytes.NewBuffer(newB)

					r.ContentLength = int64(jsonBuf.Len())
					r.Header.Set("Content-Length", strconv.FormatInt(r.ContentLength, 10))
					r.Body = io.NopCloser(jsonBuf)
				}
			}
		}

		locationHeader := r.Header.Get("location")
		if slug != "" && strings.Contains(locationHeader, fmt.Sprintf("/v2/%s", slug)) {
			r.Header.Set("location", strings.ReplaceAll(locationHeader, fmt.Sprintf("/v2/%s", slug), "/v2"))
		}

		return nil
	}

	router.HandleFunc("/", proxy.ServeHTTP)

	return router
}

func main() {
	host := os.Getenv("SSH_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	port := os.Getenv("SSH_PORT")
	if port == "" {
		port = "2222"
	}
	keyPath := os.Getenv("SSH_AUTHORIZED_KEYS")
	if keyPath == "" {
		keyPath = "ssh_data/authorized_keys"
	}
	logger := slog.Default()

	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%s", host, port)),
		wish.WithHostKeyPath("ssh_data/term_info_ed25519"),
		wish.WithAuthorizedKeys(keyPath),
		tunkit.WithWebTunnel(tunkit.NewWebTunnelHandler(serveMux, logger)),
	)

	if err != nil {
		logger.Error("could not create server", "err", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	logger.Info("starting SSH server", "host", host, "port", port)
	go func() {
		if err = s.ListenAndServe(); err != nil {
			logger.Error("serve error", "err", err)
			os.Exit(1)
		}
	}()

	<-done
	logger.Info("stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil {
		logger.Error("shutdown", "err", err)
		os.Exit(1)
	}
}
