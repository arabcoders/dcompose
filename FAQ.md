# FAQ

## How do I install the native services?

See the [native service setup and systemd units](packaging/README.md).

## How do I use existing stacks?

Point the node's `global.stacks_dir` at their parent directory. Each stack must be a real directory directly
inside it, containing `compose.yaml`, `compose.yml`, `docker-compose.yaml` or `docker-compose.yml`.
Names use 1-128 lowercase letters, digits, underscores or hyphens, starting with a letter or digit.

The directory name becomes the Compose project name. Match the existing project name before running commands
to avoid creating a separate deployment. DCompose selects the first filename in the order above, it doesn't
automatically merge override files. Keep relative bind paths valid and give the node access to any supporting files.

## How do I reset my password?

Replace `admin` with your username. For a native install, use your service account and configuration path.

### Native service

```sh
sudo systemctl stop dcompose
sudo -u dcompose /usr/local/bin/dcompose --config /etc/dcompose/dcompose.toml --set-password --username admin
sudo systemctl start dcompose
```

### Docker Compose

Run from the directory containing `compose.yaml`, keeping your existing `DCOMPOSE_UID` and `DCOMPOSE_GID` values.

```sh
docker compose stop dcompose
docker compose run --rm --no-deps dcompose dcompose --set-password --username admin
docker compose start dcompose
```

## Where can I read logs?

Open **Logs** in the header, the logs are persisted in `logs/` beside `dcompose.toml`, with one current 10 MB file and 
three rotated backups. The stack terminal follows container logs and. Command output is live-only, not replayed after 
navigation, failed command summaries appear in **Logs**.

**Needs attention** highlights issues within your stacks like dead, unhealthy running containers and nonzero exits.

## Why can't my node connect?

Check these first:

- **Address:** use HTTPS and a port the server can reach (`9077` by default). Publish the port and allow the server 
  through the firewall. Use the external address if NAT or a proxy changes it. `localhost` points to the container 
  itself, `dagent:9077` only works on the supplied Compose network.
- **Pairing:** enter the **Pairing token** from the node's startup logs in **Nodes > Add node**, not its API key.
- **After pairing:** check `auth.allowed_ips` in [agent.toml](dagent.example.toml), the saved TLS fingerprint and 
  server/node API compatibility. Pairing can succeed while `allowed_ips` blocks later requests.

Read both logs for the rejection:

```sh
docker compose logs -f dcompose dagent
```

```sh
sudo journalctl -u dcompose -u dagent -f
```

## How do I pair a node again?

Click **Disconnect** in **Nodes** to reset pairing. Copy the new pairing token from the agent logs into **Nodes > Add node**.

If you've lost the server configuration or **Disconnect** can't reset pairing, stop the agent, set `paired = false`
under `[global]` in `agent.toml`, and restart it. Use the new token from its logs to add the node again. Leave `node_id`, 
the API key and TLS credentials unchanged.

## Can I pause a node?

Use **Nodes > Pause** to pause the node and stop DCompose's access to it. Pausing node doesnt stop containers in the 
node, it simply stops DCompose from interacting with it, when you click **Resume** DCompose resumes access.

## Why is my stack's bind mount empty?

Use the same absolute stack path on the host and inside the node, such as `/opt/stacks:/opt/stacks`.
Compose resolves relative bind mounts from the stack directory. Different host and container paths can point mounts
at an empty directory. Keep `[global] stacks_dir` and both sides of the node's bind mount identical.

## Can a node update its own stack?

Yes. Put DCompose's `compose.yaml` in a `dcompose` folder in your stacks directory. Keep the `DCOMPOSE_AGENT=true`
label on the node, keep `/cache` in a volume, and use [the same stack paths on the host and in the container](#why-is-my-stacks-bind-mount-empty).
The [supplied Compose file](compose.yaml) has these settings. Don't put the node label on other containers.

For native nodes, [update binaries on the host](installation.md#upgrading); no label is needed. Failed updates can leave 
a node offline with no automatic rollback. During a containerized self-update, a sidecar keeps a temporary recovery 
journal so command output can be relayed after the node reconnects.

## How do I enable automatic updates?

Add these labels to a service in its Compose file:

```yaml
services:
  app:
    image: example/app:latest
    labels:
      io.dcompose.check-interval: 30m
      io.dcompose.auto-update: 'true'
```

The interval must be at least one minute. A service with its own interval is left out of the global checker.
Without `auto-update`, it only reports available updates. Without a custom interval, automatic updates use the
node's `image_check_interval`.

Only the selected service is pulled and recreated, other services and dependencies are left alone. Stopped services
aren't started automatically. The server schedules checks and automatic updates, disconnected nodes wait until they 
reconnect.

Configure logins in **Settings > Registries**. Custom timers, manual checks and pulls can use any matching login.
The global checker only uses logins with background checks enabled.

The server requests checks through the agents, so registries only need to be reachable from their nodes. It shares
cached image indexes across nodes and platforms, digest comparisons remain platform-specific. Registry rate limits
pause checks for the affected registry/login. Docker or Podman on the node still pulls and recreates services.

## What do stack actions do?

- **Start / Deploy** runs `compose up -d`, applying saved configuration and creating or starting containers.
- **Restart** restarts containers without applying configuration changes.
- **Pull** downloads images without recreating containers.
- **Update** pulls images and recreates containers if a running image changed. A fully stopped stack stays stopped;
  a stack-wide update can bring up stopped services too. A service-only update leaves other services alone.

## What does deleting a stack remove?

- **Stop** stops containers without removing them.
- **Bring down** removes containers and Compose-managed networks. It keeps configuration files and volumes.
- **Delete stack** also removes its managed Compose file and `.env`, then the directory if empty. Volumes, bind-mounted 
  data and unrelated files stay.

Removing a container loses data stored only in its writable layer. If the directory has multiple recognized Compose
files, DCompose refuses deletion without stopping anything. Resolve the extra files on the host first.

## How do I copy a stack to another node?

Choose **Create copy** on the stack page. It copies the saved Compose and `.env` files, Prepare destination paths, 
volumes, networks and supporting files before starting the copy.

The editor's `.env` supplies Compose interpolation. To pass variables into containers, use Compose `environment` or 
`env_file`. Except for `compose` and `.env` other files aren't copied.

## How do I back up and restore DCompose?

Open **Backup** in the account menu, select nodes and set a password to download an encrypted ZIP. It contains 
`dcompose.toml` and the selected nodes' Compose/`.env` files under `nodes/<node-id>/`. The server configuration includes 
credentials for all configured nodes and registries, regardless of the node selection.

This isn't an application-data backup. Back up volumes, bind-mounted data, supporting files and each agent's 
configuration and TLS credentials separately.

There is no restore button. Extract the archive with an AES-compatible ZIP tool, stop the affected services and restore
`dcompose.toml` to the server's configuration directory. Restore the contents of each `nodes/<node-id>/` into that node's
`stacks_dir`. Keep restored files owned by the service account and configuration/secrets private, then restart the 
services. Restoring files doesn't deploy stacks or restore their data.

## Why do live updates fail behind my reverse proxy?

Enable WebSocket upgrades and long-lived connections at `/api/events` for status, logs, operation output and shells.
In `dcompose.toml`, set `[global] origin` to your browser-facing HTTPS origin without a path and `secure_cookie = true`.
Restart the server.

If you also proxy a node, enable upgrades at `/channel`, preserve bearer authentication and use the TLS public key
pinned in that node's connection settings. In `agent.toml`, set `auth.trusted_proxies` to the proxy's address and
`auth.allowed_ips` to the DCompose server's address, not the browser client's. Forward the server's address in
`X-Forwarded-For`. For Nginx:

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
```

You don't need these node proxy settings when only the DCompose web server is behind a proxy.
