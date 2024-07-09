# tunkit - ssh tunnel tooling

- Passwordless authentication for the browser using SSH local forwarding.
- Pub/sub system using SSH remote forwarding.
- Implemented as [wish](https://github.com/charmbracelet/wish) middleware.

# Passwordless authentication

The end-user creates a local forward SSH tunnel to a service running `tunkit`.
`tunkit` spins up a dedicated web service for that tunnel -- using a unix
socket. Then the user can access that web service using `localhost`. The web
service can then access the SSH context in order to know who the user is and
what they are authorized to do within the web service.

## Why?

Sometimes all you have is an ssh keypair for authenticating a user and don't
want to require them to create a completely separate auth mechanism for website
access.

For example, have you ever wished you could use `docker push` and `docker pull`
using just an SSH keypair? Well now it's possible.

We built this library to support [imgs.sh](https://pico.sh/imgs): a private
docker registry leveraging SSH tunnels.

# Pub/sub system

Use an SSH tunnels for "webhooks":

- Integrate the publisher middleware into an SSH server
- A user can start an http server on localhost
- User can initial an SSH remote tunnel to SSH server
- Publisher emits events by `http.Get` the user's local http server

## Why?

The biggest benefit is the user's http server is not public. There's zero
concern for malicious actors or bots trying to hit a user's event endpoints.
This dramatically reduces the infrastructure requirements for the end-user. They
just need to start an http server and initial a tunnel to a service.

# Examples

Checkout our examples folder.

```bash
go run ./cmd/example
```

```bash
ssh -L 0.0.0.0:1338:localhost:80 \
		-p 2222 \
		-o UserKnownHostsFile=/dev/null \
		-o StrictHostKeyChecking=no \
		-N \
		localhost
```

Go to http://localhost:1338
