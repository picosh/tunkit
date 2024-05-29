package main

import (
	"fmt"
	"log/slog"
	"net/http"
)

func main() {
	host := "0.0.0.0"
	port := "3000"
	logger := slog.Default()

	router := http.NewServeMux()
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body := fmt.Sprintf("received event: (path:%s, query:%s)", r.URL.Path, r.URL.Query())
		_, err := w.Write([]byte(body))
		logger.Info("response", "body", body)
		if err != nil {
			logger.Error("error writing response", "err", err)
		}
	})

	listen := fmt.Sprintf("%s:%s", host, port)
	logger.Info("server running\n", "url", listen)
	err := http.ListenAndServe(
		listen,
		router,
	)
	if err != nil {
		logger.Error("http serve", "err", err)
	}
}
