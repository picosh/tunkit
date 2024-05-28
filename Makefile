fmt:
	go fmt ./...
.PHONY: fmt

lint:
	golangci-lint run -E goimports -E godot --timeout 10m
.PHONY: lint

build-example:
	go build -o ./build/example ./cmd/example
.PHONY: build

build-docker:
	go build -o ./build/docker ./cmd/docker
.PHONY: build-docker

build-pubsub:
	go build -o ./build/pub ./cmd/pubsub/pub
	go build -o ./build/sub ./cmd/pubsub/sub
.PHONY: build-pubsub

tunnel:
	ssh -L 0.0.0.0:5000:localhost:80 \
		-p 2222 \
		-o UserKnownHostsFile=/dev/null \
		-o StrictHostKeyChecking=no \
		-N \
		localhost
.PHONY: tunnel

tunnel-prod:
	ssh -L 0.0.0.0:1338:localhost:1338 -N imgs.sh
	# docker pull ubuntu
	# docker tag ubuntu localhost:1338/ubuntu
	# docker push localhost:1338/ubuntu
.PHONY: tunnel
