# Manual installation

For guided setup, use the [installer](../installation.md).

For native installation, download `dcompose` for the server and `dagent` for each managed host from
[Releases](https://github.com/arabcoders/dcompose/releases), or [build them](../CONTRIBUTING.md#build-from-source).
These steps are for a new installation. Run them from the extracted archive. Skip `useradd` if the account already exists.

## Server setup

Keep port `8080` private until you've created your account.

```sh
sudo useradd --system --user-group --no-create-home --home-dir /etc/dcompose --shell /usr/sbin/nologin dcompose
sudo install -d -o dcompose -g dcompose -m 0700 /etc/dcompose
sudo install -o root -g root -m 0755 dcompose /usr/local/bin/dcompose
sudo install -o root -g root -m 0644 packaging/systemd/dcompose.service /etc/systemd/system/dcompose.service
sudo systemctl daemon-reload
sudo systemctl enable --now dcompose
```

Open `http://<server-ip>:8080` and create your account. Configuration is in `/etc/dcompose/dcompose.toml`.
The server doesn't need Docker or Podman.

If you move `/etc/dcompose`, change `DCOMPOSE_CONFIG_DIR` and `ReadWritePaths` in the service file too.

## Docker node setup

Install Docker with Compose 2.35 or later. For Podman, skip to [Podman nodes](#podman-nodes).

```sh
sudo useradd --system --user-group --no-create-home --home-dir /var/lib/dagent --shell /usr/sbin/nologin dagent
sudo install -d -o dagent -g dagent -m 0700 /var/lib/dagent/.config/dcompose
sudo install -d -o dagent -g dagent -m 0750 /opt/stacks
sudo install -d -o root -g root -m 0755 /opt/dagent
sudo install -o root -g root -m 0755 dagent /opt/dagent/dagent
sudo install -o root -g root -m 0644 packaging/systemd/dagent.service /etc/systemd/system/dagent.service
sudo systemctl daemon-reload
sudo systemctl enable --now dagent
```

Configuration is in `/var/lib/dagent/.config/dcompose/agent.toml`. If you have existing stacks, give `dagent` access
to their directories, Compose files and `.env` files. Don't change ownership of application data to do this.

Allow the server through the firewall on port `9077`. Read the **Pairing token** with `sudo journalctl -u dagent`,
then enter it and the node's address in **Nodes > Add node**. See the [FAQ](../FAQ.md#why-cant-my-node-connect) if it won't connect.

## Credentials and permissions

Keep binaries and service files owned by root. Give each service account ownership of its configuration directory
with mode `0700`, and keep configuration and secret files `0600`.

To add a secret such as an OIDC client secret after the first start:

```sh
sudo install -o dcompose -g dcompose -m 0600 /path/to/oidc-client-secret /etc/dcompose/secrets/oidc-client-secret
```

Set `auth.oidc.client_secret` to `file:secrets/oidc-client-secret`. Relative paths start beside the TOML file.

Add registry logins in **Settings > Registries**, or import Docker's `config.json` there. DCompose sends them to the
nodes; no per-node `docker login` or host configuration mount is needed.

Restart the service after changing configuration or secret files. Run commands such as
[password resets](../FAQ.md#how-do-i-reset-my-password) as the service account, not root.
See the [server](../dcompose.example.toml) and [node](../dagent.example.toml) examples for other settings.

## Podman nodes

Install Podman, the Docker CLI and Docker Compose 2.35 or later. You'll use `docker compose`, not `podman-compose`.
You don't need a Docker daemon. Only Unix sockets are supported.

Start the socket on the host before setting up the node.

For rootless Podman, run these as the user who owns the containers:

```sh
systemctl --user enable --now podman.socket
systemctl --user enable podman-restart.service
sudo loginctl enable-linger "$USER"
export PODMAN_SOCKET="$XDG_RUNTIME_DIR/podman/podman.sock"
```

For rootful Podman, use a root shell:

```sh
sudo -s
systemctl enable --now podman.socket
systemctl enable podman-restart.service
export PODMAN_SOCKET=/run/podman/podman.sock
```

### Containerized setup

Download [compose.yaml](../compose.yaml). In `dagent`, keep the label below, add the environment and security settings,
and replace the Docker socket mount with the Podman socket mount:

```yaml
labels:
  DCOMPOSE_AGENT: 'true'
environment:
  - CONTAINER_HOST=unix:///run/podman/podman.sock
security_opt:
  - label=disable
volumes:
  - type: bind
    source: ${PODMAN_SOCKET}
    target: /run/podman/podman.sock
```

Leave the configuration, cache and stack mounts in place. Create `config/dagent`, save the
[node example](../dagent.example.toml) as `config/dagent/agent.toml`, and set `engine = "podman"` under `[global]`.
Create `/opt/stacks` and give the node write access to it.

If you're also running the server:

- Create `config/dcompose` owned by the UID/GID set for `dcompose` in the Compose file.
- For rootless Podman, set `dcompose`'s `user` to `'0:0'` and own that directory as your login user.
- On SELinux hosts, use `./config/dcompose:/config:Z` for the server's configuration mount.

Generate the node's credentials and start the containers:

```sh
export DOCKER_HOST="unix://$PODMAN_SOCKET"
unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
docker compose run --rm --no-deps -T dagent dagent --generate-cert /config --generate-token /config/secrets/token
docker compose up -d
```

Read the pairing token with `docker compose logs dagent` and add the node in the web interface.
For updates from the web interface, see [updating the node's own stack](../FAQ.md#can-a-node-update-its-own-stack).

### Rootless setup

For a native rootless node, use your existing login account. From the extracted `dagent` archive:

```sh
sudo install -d -o root -g root -m 0755 /opt/dagent
sudo install -o root -g root -m 0755 dagent /opt/dagent/dagent
install -d -m 0700 "$HOME/.config/dcompose" "$HOME/.config/systemd/user"
install -m 0600 dagent.example.toml "$HOME/.config/dcompose/agent.toml"
sudo install -d -m 0750 -o "$(id -u)" -g "$(id -g)" /opt/stacks
/opt/dagent/dagent --generate-cert "$HOME/.config/dcompose" --generate-token "$HOME/.config/dcompose/secrets/token"
```

In `~/.config/dcompose/agent.toml`, set `engine = "podman"` under `[global]`. Change `stacks_dir` if you don't use `/opt/stacks`.

Create `~/.config/systemd/user/dagent.service` with:

```ini
[Unit]
Description=DCompose Podman node
Requires=podman.socket
After=podman.socket

[Service]
ExecStart=/opt/dagent/dagent --config %h/.config/dcompose/agent.toml
Restart=on-failure
RestartSec=5
UMask=0027

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now dagent
journalctl --user -u dagent
```

The pairing token is in the journal output. Rootless stats need cgroups v2; some network setups don't report network stats.

### Rootful setup

For a native rootful node, run `dagent` as root. From the extracted `dagent` archive:

```sh
sudo install -d -m 0755 /opt/dagent
sudo install -m 0755 dagent /opt/dagent/dagent
sudo install -d -m 0700 /var/lib/dagent/.config/dcompose
sudo install -m 0600 dagent.example.toml /var/lib/dagent/.config/dcompose/agent.toml
sudo install -d -m 0750 /opt/stacks
sudo install -m 0644 packaging/systemd/dagent.service /etc/systemd/system/dagent.service
```

In `/etc/systemd/system/dagent.service`:

- Set `User=root` and `Group=root`.
- Remove `SupplementaryGroups=docker`.
- Replace `docker.service` with `podman.socket` in `After` and `Requires`.

In `/var/lib/dagent/.config/dcompose/agent.toml`, set `engine = "podman"` under `[global]`.
Generate credentials, then start the service:

```sh
sudo /opt/dagent/dagent --generate-cert /var/lib/dagent/.config/dcompose --generate-token /var/lib/dagent/.config/dcompose/secrets/token
sudo systemctl daemon-reload
sudo systemctl enable --now dagent
sudo journalctl -u dagent
```

### Private registries

Use **Settings > Registries** for Docker and Podman nodes alike. Image checks don't read Podman's `registries.conf`
or `certs.d`; use a full registry address in your image names.
