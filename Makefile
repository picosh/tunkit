fmt:
	go fmt ./...
.PHONY: fmt

lint:
	golangci-lint run -E goimports -E godot --timeout 10m
.PHONY: lint

tunnel:
	ssh -L 0.0.0.0:1338:localhost:80 \
		-p 2222 \
		-o UserKnownHostsFile=/dev/null \
		-o StrictHostKeyChecking=no \
		-N \
		localhost
.PHONY: tunnel
