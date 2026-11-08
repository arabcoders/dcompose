# Contributing

DCompose is my own little pet project that was inspired by the [Dockge](https://github.com/louislam/dockge). I have 
specific requirements and preferences for how this project is developed and maintained. Discuss your change and get 
explicit approval before starting work or opening a pull request. Unsolicited pull requests will be closed without 
review.

## Before you start

Report vulnerabilities privately through [SECURITY.md](SECURITY.md). For a bug fix or small feature, open a GitHub issue.
Use a discussion for larger changes or general questions. Explain what you want to change, why, your proposed approach
and which existing behavior or use cases it affects.

Bug fixes, functional improvements and measurable performance work take priority. An interface change needs more than
personal preference. I will decline a proposal or ask for a different scope or approach.

## AI-assisted work

AI tools are allowed, you don't need to disclose their use. You're responsible for every change: understand it, review
it against the existing code, test it and be able to explain how it works and why. Unreviewed generated code, tests or
documentation will be rejected. Write tests that check behavior, not just increase coverage.

## Development setup

You'll need Go 1.27.3+ and Bun 1.4+. Run commands from the repository root. Install frontend dependencies with:

```sh
bun --cwd=$PWD/frontend/ install --frozen-lockfile
```

Install the local pre-commit hook once per clone:

```sh
.githooks/install
```

```sh
.githooks/pre-commit             # staged files
.githooks/pre-commit --unstaged  # unstaged and untracked files
.githooks/pre-commit --all       # all tracked files
```

The installer preserves an existing pre-commit hook unless you pass `--force`. To keep local checks, place executable
files in `.git/hooks/pre-commit.d/` (always run), `backend/` (Go changes), or `frontend/` (frontend changes).

## Check changes

Check the code you changed. For the frontend:

```sh
bun --cwd=$PWD/frontend/ lint
bun --cwd=$PWD/frontend/ lint:tsc
bun --cwd=$PWD/frontend/ test
bun --cwd=$PWD/frontend/ format:check
```

For Go changes:

```sh
go test -race ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
```

## Local hot reload

Install the tools from `mise.toml` and start the backend watcher:

```sh
mise install
mise run dev:server
```

The watcher builds the backend without an embedded frontend. See [dcompose.example.toml](dcompose.example.toml) for
configuration lookup. Keep your configuration and secrets between restarts. To choose a different file:

```sh
mise run dev:server --config /path/to/dcompose.toml
```

The tasks inherit your shell environment, including `DCOMPOSE_CONFIG_DIR`, they don't load `.env` or token files.
Run Nuxt in another terminal:

```sh
NUXT_API_URL=http://127.0.0.1:8080/api/ bun --cwd=$PWD/frontend/ dev
```

Open `http://localhost:8082` and leave secure cookies disabled for HTTP. For OIDC, register
`http://localhost:8082/api/auth/oidc/callback` and set `auth.oidc.redirect_uri` to that URL.

Restart the backend after editing runtime configuration. A failed Go build leaves it stopped until a build succeeds.

### Optional node

Run the node watcher in another terminal if you need it:

```sh
mise run dev:agent --config /path/to/agent.toml
```

The node needs Docker or Podman, Docker Compose 2.35+ and a writable stack directory. See
[node setup](packaging/README.md#docker-node-setup) for runtime requirements and permissions. Node restarts interrupt
active operations, logs and shells.

## Build from source

Generate the frontend and embed it in the server binary:

```sh
bun --cwd=$PWD/frontend/ generate
go build -tags embed -o dist/dcompose ./cmd/dcompose
go build -o dist/dagent ./cmd/dagent
```

The deployed server includes the web interface and doesn't need Go or Bun.

## API versions

`GET /api/version` returns the server's application version, API version and supported node API version. A node's
authenticated `/health` response returns its application version, API version and host details.

API versions live in `internal/protocol`, separate from application versions. Bump them for breaking protocol changes.
Nodes reject a mismatched `X-Dcompose-API-Version` header except on `/health`.

## Build the container image

The image includes both binaries, Docker Compose and the Podman remote client. It runs `dcompose` by default, override
the command with `dagent` for a node. To build it:

```sh
docker build -t arabcoders/dcompose .
```
