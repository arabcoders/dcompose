# Installation

## Requirements

- Linux amd64 or arm64, Bash, curl, coreutils and sudo.
- **Compose:** Docker or Podman, plus the Docker CLI and Compose plugin 2.35+.
- **Native services:** systemd, unzip and sha256sum. Nodes also need Docker or Podman, the Docker CLI and Compose plugin 2.35+.
- **Podman:** systemd. Use `docker compose`, not `podman-compose` or the `podman-docker` wrapper. A Docker daemon isn't needed.
- **Server + node:** OpenSSL.

Install these dependencies first, the installer doesn't install them. Set up an HTTPS reverse proxy for network access
to the web interface and allow only the server's addresses through the remote node's firewall.

## Run the installer

Use your normal account, not `sudo bash`. For rootless Podman, use the account that owns the containers.
The installer is for fresh installations only. It won't overwrite existing configuration, services or binaries.

```sh
curl -fL https://raw.githubusercontent.com/arabcoders/dcompose/master/install.sh -o install.sh
bash install.sh
```

Running without flags previews the installation. To install:

```sh
bash install.sh --execute
```

Use `bash install.sh --help` to see all options.

## Choose the deployment

- **Server + node:** run the web interface and manage containers on this host. They're linked automatically.
- **Server only:** run the web interface, add nodes afterward.
- **Node only:** manage this host from a DCompose server you already have.

Choose **Compose** or **native systemd**. Both Docker and Podman are supported, including rootless and rootful Podman.
Choose your stack and installation directories when prompted.

Compose uses `ghcr.io/arabcoders/dcompose:latest` by default. Native installs use the latest
[GitHub Release](https://github.com/arabcoders/dcompose/releases). To choose a specific version:

```sh
bash install.sh --execute --version TAG
```

Replace `TAG` with a published release tag. For Compose, use `--version dev` to install the development image.
If you're installing the server, the wizard asks you to create an account before starting it.

## After installation

Open the URL printed at the end and sign in with the account you created. The summary also shows where the files
are stored and how to read the logs.

For **Node only**, open **Nodes > Add node** on your server. Enter the node's HTTPS address and the single-use
**Pairing token** from its logs. Keep the token private. The address and port (`9077` by default) must be reachable
from the server, use the public address if NAT or a proxy changes it.

To verify the node's identity before pairing, optionally copy its **TLS fingerprint** from the agent console 
into **TLS fingerprint**. A mismatch stops pairing before the token is sent. Leave it blank to pair without 
pre-verifying the fingerprint.

## Manual installation and reference

- [Docker Compose file](compose.yaml) and [manual Docker setup](README.md#docker-deployment)
- [Native services](packaging/README.md) and [manual Podman setup](packaging/README.md#podman-nodes)
- [Server configuration](dcompose.example.toml) and [node configuration](dagent.example.toml)
- [FAQ](FAQ.md) for pairing, permissions and reverse proxy troubleshooting

## Upgrading

Back up configuration, secrets and application data first. The installer doesn't upgrade existing installations.

For Compose, change the image tags in `compose.yaml` if you want a specific release, then run `docker compose pull`
and `docker compose up -d` from its directory. The `latest` tag tracks releases; `dev` tracks ongoing development.

For native services, download the release archive for your architecture and verify its binary with
`sha256sum --check SHA256SUMS` from the extracted directory. For the server:

```sh
sudo systemctl stop dcompose
sudo install -o root -g root -m 0755 dcompose /usr/local/bin/dcompose
sudo systemctl start dcompose
```

For a node, use `dagent` as the service and binary name, installing it at `/opt/dagent/dagent`.
Rootless Podman nodes use `systemctl --user` without sudo. Keep configuration and credentials in place.
Check the service logs and node connection after restarting, keep the previous binary if you need to revert.
