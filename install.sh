#!/usr/bin/env bash
set -euo pipefail

RELEASES=https://github.com/arabcoders/dcompose/releases
VERSION=
ROLE=both
METHOD=compose
ENGINE=docker
PODMAN_MODE=rootless
STACKS_DIR=/opt/stacks
WEB_PORT=8080
NODE_PORT=9077
HOST=
SERVER_SOURCE=any
TEMP_DIR=

fail() {
    printf 'Error: %s\n' "$*" >&2
    exit 1
}

need() {
    command -v "$1" >/dev/null || fail "Install $1 and run the installer again."
}

as_root() {
    if [[ $EUID == 0 ]]; then
        "$@"
    else
        sudo "$@"
    fi
}

as_user() {
    local user=$1
    shift
    if [[ $(id -un) == "$user" ]]; then
        "$@"
    else
        as_root runuser -u "$user" -- "$@"
    fi
}

docker_cli() {
    if [[ $ENGINE == podman ]]; then
        local connection=(env "DOCKER_HOST=unix://$PODMAN_SOCKET" DOCKER_CONTEXT= DOCKER_TLS_VERIFY= DOCKER_CERT_PATH=)
        if [[ $PODMAN_MODE == rootful ]]; then
            as_root "${connection[@]}" docker "$@"
        else
            "${connection[@]}" docker "$@"
        fi
    else
        docker "$@"
    fi
}

prepare_podman_socket() {
    need systemctl
    if [[ $PODMAN_MODE == rootless ]]; then
        as_root loginctl enable-linger "$(id -un)"
        systemctl --user enable --now podman.socket
        systemctl --user enable podman-restart.service
    else
        as_root systemctl enable --now podman.socket
        as_root systemctl enable podman-restart.service
    fi
    as_root test -S "$PODMAN_SOCKET" || fail "No Podman socket at $PODMAN_SOCKET. Start that socket before installing."
    docker_cli info >/dev/null
}

ask() {
    local variable=$1 label=$2 default=$3 answer
    read -r -p "$label [$default]: " answer </dev/tty || fail 'Installation cancelled.'
    printf -v "$variable" '%s' "${answer:-$default}"
}

choose() {
    local variable=$1 label=$2 default=$3 option
    shift 3
    while true; do
        ask "$variable" "$label" "$default"
        for option in "$@"; do
            if [[ ${!variable} == "$option" ]]; then
                return
            fi
        done
        printf 'Choose one of: %s\n' "$*" >&2
    done
}

set_admin_password() {
    local retry
    until "$@" </dev/tty; do
        choose retry 'Retry password setup (yes/no)' yes yes no
        [[ $retry == yes ]] || fail 'Password setup cancelled. Installation files remain; services were not started.'
    done
}

toml_string() {
    local value=$1
    value=${value//\\/\\\\}
    value=${value//\"/\\\"}
    printf '"%s"' "$value"
}

yaml_string() {
    local value=$1
    value=${value//\'/\'\'}
    value=${value//\$/\$\$}
    printf "'%s'" "$value"
}

absolute_path() {
    [[ $1 == /* && $1 != *$'\n'* && $1 != *$'\r'* ]] || fail 'Use an absolute directory path.'
    realpath -m -- "$1"
}

port() {
    if [[ $1 =~ ^[0-9]{1,5}$ ]] && ((10#$1 >= 1 && 10#$1 <= 65535)); then
        return
    fi
    fail 'Ports must be between 1 and 65535.'
}

refuse_existing() {
    local path
    for path in "$@"; do
        if as_root test -e "$path" || as_root test -L "$path"; then
            fail "$path already exists. This installer doesn't replace existing installations."
        fi
    done
}

create_account() {
    if ! id "$1" >/dev/null 2>&1; then
        as_root useradd --system --user-group --no-create-home --home-dir "$2" --shell /usr/sbin/nologin "$1"
    fi
}

prepare_stacks() {
    if ! as_root test -d "$STACKS_DIR"; then
        as_root install -d -m 0750 -o "$NODE_USER" -g "$NODE_GROUP" "$STACKS_DIR"
    fi
    if ! as_user "$NODE_USER" test -r "$STACKS_DIR" || ! as_user "$NODE_USER" test -w "$STACKS_DIR" || ! as_user "$NODE_USER" test -x "$STACKS_DIR"; then
        fail "Give $NODE_USER read/write access to $STACKS_DIR, then run the installer again. Existing file ownership wasn't changed."
    fi
}

write_agent_config() {
    printf '[global]\nengine = "%s"\nlisten = "%s"\nstacks_dir = ' "$ENGINE" "$AGENT_LISTEN"
    toml_string "$STACKS_DIR"
    printf '\npaired = %s\n\n[auth]\ntoken = "file:secrets/token"\nallowed_ips = ' "$PAIRED"
    if [[ $ROLE == both && $METHOD == compose ]]; then
        printf '["dcompose"]'
    elif [[ $ROLE == both ]]; then
        printf '["127.0.0.1", "::1"]'
    elif [[ $SERVER_SOURCE == any ]]; then
        printf '["0.0.0.0/0", "::/0"]'
    else
        printf '['
        toml_string "$SERVER_SOURCE"
        printf ']'
    fi
    printf '\n\n[tls]\ncert_file = "node.crt"\nkey_file = "node.key"\n'
}

write_server_config() {
    printf '[global]\nlisten = "%s"\n' "$SERVER_LISTEN"
    if [[ $ROLE == both ]]; then
        printf '\n[nodes.local]\nname = '
        toml_string "$HOST"
        printf '\nurl = "%s"\ntoken = "file:secrets/local-node.token"\nfingerprint = "%s"\npublic_host = ' "$NODE_URL" "$FINGERPRINT"
        toml_string "$HOST"
        printf '\n'
    fi
}

write_compose() {
    local server_identity=$INSTALL_UID:$INSTALL_GID server_mount=./config/dcompose:/config
    if [[ $ENGINE == podman ]]; then
        server_mount+=:Z
        if [[ $PODMAN_MODE == rootless ]]; then
            server_identity=0:0
        fi
    fi
    printf 'name: dcompose\n\nservices:\n'
    if [[ $ROLE != node ]]; then
        cat <<EOF
  dcompose:
    image: arabcoders/dcompose:$VERSION
    user: '$server_identity'
    restart: unless-stopped
    init: true
    read_only: true
    tmpfs: [/tmp]
    ports:
      - '0.0.0.0:$WEB_PORT:8080'
    volumes:
      - $server_mount
EOF
    fi
    if [[ $ROLE != server ]]; then
        cat <<EOF
  dagent:
    image: arabcoders/dcompose:$VERSION
    hostname: dagent
    command: [dagent]
    labels:
      DCOMPOSE_AGENT: 'true'
    restart: unless-stopped
    init: true
    read_only: true
    tmpfs: [/tmp]
EOF
        if [[ $ENGINE == podman ]]; then
            cat <<'EOF'
    environment:
      CONTAINER_HOST: unix:///run/podman/podman.sock
    security_opt:
      - label=disable
EOF
        fi
        if [[ $ROLE == node ]]; then
            printf "    ports:\n      - '0.0.0.0:%s:9077'\n" "$NODE_PORT"
        fi
        cat <<'EOF'
    volumes:
      - ./config/dagent:/config
      - dagent_cache:/cache
      - type: bind
EOF
        printf '        source: '
        if [[ $ENGINE == podman ]]; then
            yaml_string "$PODMAN_SOCKET"
            printf '\n        target: /run/podman/podman.sock\n'
        else
            yaml_string "$DOCKER_SOCKET"
            printf '\n        target: /var/run/docker.sock\n'
        fi
        printf '      - type: bind\n        source: '
        yaml_string "$STACKS_DIR"
        printf '\n        target: '
        yaml_string "$STACKS_DIR"
        printf '\n\nvolumes:\n  dagent_cache:\n'
    fi
}

write_podman_unit() {
    cat <<'EOF'
[Unit]
Description=DCompose Podman node
Requires=podman.socket
After=podman.socket

[Service]
EOF
    if [[ $PODMAN_MODE == rootful ]]; then
        printf 'User=root\nGroup=root\n'
    fi
    printf 'ExecStart=/opt/dagent/dagent --config "%s"\n' "${NODE_CONFIG//%/%%}"
    printf 'Restart=on-failure\nRestartSec=5\nTimeoutStopSec=20\nUMask=0027\n\n[Install]\n'
    if [[ $PODMAN_MODE == rootless ]]; then
        printf 'WantedBy=default.target\n'
    else
        printf 'WantedBy=multi-user.target\n'
    fi
}

download_binary() {
    local binary=$1 package="$1-linux-$ARCH-$VERSION"
    curl -fsSL "$RELEASES/download/$VERSION/$package.zip" -o "$TEMP_DIR/$package.zip" ||
        fail "Couldn't download $package. Choose a published release with --version, or use the Compose installation."
    unzip -q "$TEMP_DIR/$package.zip" -d "$TEMP_DIR" || fail "Couldn't unpack $package."
    (cd "$TEMP_DIR/$package" && sha256sum --check SHA256SUMS) || fail "Checksum verification failed for $binary."
}

fingerprint() {
    as_root openssl x509 -in "$NODE_DIR/node.crt" -pubkey -noout |
        openssl pkey -pubin -outform DER |
        openssl dgst -sha256 -binary |
        openssl base64 -A
}

save_configs() {
    if [[ $ROLE != server ]]; then
        write_agent_config >"$TEMP_DIR/agent.toml"
        as_root install -m 0600 -o "$NODE_USER" -g "$NODE_GROUP" "$TEMP_DIR/agent.toml" "$NODE_CONFIG"
    fi
    if [[ $ROLE != node ]]; then
        if [[ $ROLE == both ]]; then
            FINGERPRINT=$(fingerprint)
            as_root install -m 0600 -o "$SERVER_USER" -g "$SERVER_GROUP" "$NODE_DIR/secrets/token" "$SERVER_DIR/secrets/local-node.token"
        fi
        write_server_config >"$TEMP_DIR/dcompose.toml"
        as_root install -m 0600 -o "$SERVER_USER" -g "$SERVER_GROUP" "$TEMP_DIR/dcompose.toml" "$SERVER_CONFIG"
    fi
}

compose_install() {
    if as_root test -d "$INSTALL_DIR" && [[ -n $(as_root find "$INSTALL_DIR" -mindepth 1 -maxdepth 1 -print -quit) ]]; then
        fail "$INSTALL_DIR isn't empty. Choose a new installation directory."
    fi
    if [[ $ENGINE == podman ]]; then
        prepare_podman_socket
    fi
    if [[ -n $(docker_cli ps --all --filter label=com.docker.compose.project=dcompose --format '{{.ID}}') ]]; then
        fail 'A dcompose Compose project already exists. Use its current installation directory to manage it.'
    fi
    if [[ $ROLE != server && -n $(docker_cli ps --all --filter label=DCOMPOSE_AGENT=true --format '{{.ID}}') ]]; then
        fail 'A DCompose node container already exists on this host.'
    fi
    docker_cli pull "arabcoders/dcompose:$VERSION"
    NODE_USER=root
    NODE_GROUP=root
    if [[ $ENGINE == podman && $PODMAN_MODE == rootless ]]; then
        NODE_USER=$(id -un)
        NODE_GROUP=$(id -gn)
    fi
    SERVER_USER=$INSTALL_UID
    SERVER_GROUP=$INSTALL_GID
    if [[ $ROLE != server ]]; then
        prepare_stacks
    fi
    as_root install -d -m 0750 -o "$INSTALL_UID" -g "$INSTALL_GID" "$INSTALL_DIR"
    local compose=(docker_cli compose --project-directory "$INSTALL_DIR" -f "$INSTALL_DIR/compose.yaml")
    NODE_DIR=$INSTALL_DIR/config/dagent
    SERVER_DIR=$INSTALL_DIR/config/dcompose
    NODE_CONFIG=$NODE_DIR/agent.toml
    SERVER_CONFIG=$SERVER_DIR/dcompose.toml
    AGENT_LISTEN=0.0.0.0:9077
    SERVER_LISTEN=0.0.0.0:8080
    NODE_URL=https://dagent:9077
    write_compose >"$TEMP_DIR/compose.yaml"
    as_root install -m 0644 -o "$INSTALL_UID" -g "$INSTALL_GID" "$TEMP_DIR/compose.yaml" "$INSTALL_DIR/compose.yaml"
    if [[ $ROLE != node ]]; then
        as_root install -d -m 0700 -o "$SERVER_USER" -g "$SERVER_GROUP" "$SERVER_DIR" "$SERVER_DIR/secrets"
    fi
    if [[ $ROLE != server ]]; then
        as_root install -d -m 0700 -o "$NODE_USER" -g "$NODE_GROUP" "$NODE_DIR" "$NODE_DIR/secrets"
    fi
    if [[ $ROLE != server ]]; then
        "${compose[@]}" run --rm --no-deps -T dagent dagent --generate-cert /config --generate-token /config/secrets/token
    fi
    save_configs
    if [[ $ROLE != node ]]; then
        set_admin_password "${compose[@]}" run --rm --no-deps dcompose dcompose --set-password --username "$ADMIN_USER"
    fi
    "${compose[@]}" up -d
    printf '\nCompose file: %s/compose.yaml\n' "$INSTALL_DIR"
    if [[ $ENGINE == podman ]]; then
        if [[ $PODMAN_MODE == rootful ]]; then
            printf 'Logs: sudo '
        else
            printf 'Logs: '
        fi
        printf 'env DOCKER_HOST=%q DOCKER_CONTEXT= DOCKER_TLS_VERIFY= DOCKER_CERT_PATH= docker compose --project-directory %q -f %q logs -f\n' "unix://$PODMAN_SOCKET" "$INSTALL_DIR" "$INSTALL_DIR/compose.yaml"
    else
        printf 'Logs: docker compose --project-directory %q -f %q logs -f\n' "$INSTALL_DIR" "$INSTALL_DIR/compose.yaml"
    fi
}

native_install() {
    need systemctl
    need unzip
    need sha256sum
    need runuser
    if [[ $ROLE != server && $ENGINE == docker ]]; then
        systemctl cat docker.service >/dev/null || fail 'The native Docker node needs docker.service.'
    elif [[ $ROLE != server && $PODMAN_MODE == rootful ]]; then
        as_root podman --remote=false info >/dev/null
    fi
    local server_unit=/etc/systemd/system/dcompose.service node_unit=/etc/systemd/system/dagent.service
    SERVER_DIR=/etc/dcompose
    NODE_DIR=/var/lib/dagent/.config/dcompose
    SERVER_CONFIG=$SERVER_DIR/dcompose.toml
    NODE_USER=dagent
    NODE_HOME=/var/lib/dagent
    if [[ $ENGINE == podman && $PODMAN_MODE == rootless && $ROLE != server ]]; then
        [[ $EUID != 0 ]] || fail 'Run the installer without sudo for a rootless Podman node.'
        systemctl --user show-environment >/dev/null || fail 'Run this from a login session with a systemd user service manager.'
        NODE_USER=$(id -un)
        NODE_HOME=$HOME
        local user_config=$HOME/.config
        if [[ ${XDG_CONFIG_HOME:-} == /* ]]; then
            user_config=$XDG_CONFIG_HOME
        fi
        NODE_DIR=$user_config/dcompose
        node_unit=$user_config/systemd/user/dagent.service
    elif [[ $ENGINE == podman && $ROLE != server ]]; then
        NODE_USER=root
        NODE_HOME=/root
    fi
    NODE_CONFIG=$NODE_DIR/agent.toml
    if [[ $ROLE != node ]]; then
        refuse_existing "$SERVER_DIR" /usr/local/bin/dcompose "$server_unit"
    fi
    if [[ $ROLE != server ]]; then
        refuse_existing "$NODE_DIR" /opt/dagent/dagent "$node_unit"
    fi
    if [[ -z $VERSION ]]; then
        local latest
        latest=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$RELEASES/latest") || fail 'No latest native release is available. Use --version with a published release, or choose Compose.'
        VERSION=${latest##*/}
    fi
    [[ $VERSION =~ ^v[0-9][A-Za-z0-9._-]*$ ]] || fail 'Native installations need a published v* release tag.'
    if [[ $ROLE != node ]]; then
        download_binary dcompose
    fi
    if [[ $ROLE != server ]]; then
        download_binary dagent
    fi
    SERVER_USER=dcompose
    SERVER_GROUP=dcompose
    SERVER_LISTEN=0.0.0.0:$WEB_PORT
    AGENT_LISTEN=0.0.0.0:$NODE_PORT
    if [[ $ROLE == both ]]; then
        AGENT_LISTEN=127.0.0.1:$NODE_PORT
    fi
    NODE_URL=https://127.0.0.1:$NODE_PORT
    if [[ $ROLE != server ]]; then
        if [[ $NODE_USER == dagent ]]; then
            create_account dagent "$NODE_HOME"
        fi
        NODE_GROUP=$(id -gn "$NODE_USER")
        prepare_stacks
    fi
    if [[ $ROLE != node ]]; then
        create_account dcompose "$SERVER_DIR"
        SERVER_GROUP=$(id -gn dcompose)
        as_root install -d -m 0700 -o dcompose -g "$SERVER_GROUP" "$SERVER_DIR" "$SERVER_DIR/secrets"
        as_root install -d -m 0755 -o root -g root /usr/local/bin
        as_root install -m 0755 -o root -g root "$TEMP_DIR/dcompose-linux-$ARCH-$VERSION/dcompose" /usr/local/bin/dcompose
        as_root install -m 0644 -o root -g root "$TEMP_DIR/dcompose-linux-$ARCH-$VERSION/packaging/systemd/dcompose.service" "$server_unit"
    fi
    if [[ $ROLE != server ]]; then
        as_root install -d -m 0755 -o root -g root /opt/dagent
        if [[ $ENGINE == podman && $PODMAN_MODE == rootless ]]; then
            mkdir -p "$NODE_DIR"
        fi
        as_root install -d -m 0700 -o "$NODE_USER" -g "$NODE_GROUP" "$NODE_DIR" "$NODE_DIR/secrets"
        as_root install -m 0755 -o root -g root "$TEMP_DIR/dagent-linux-$ARCH-$VERSION/dagent" /opt/dagent/dagent
        as_user "$NODE_USER" env HOME="$NODE_HOME" /opt/dagent/dagent --generate-cert "$NODE_DIR" --generate-token "$NODE_DIR/secrets/token"
        if [[ $ENGINE == docker ]]; then
            as_root install -m 0644 -o root -g root "$TEMP_DIR/dagent-linux-$ARCH-$VERSION/packaging/systemd/dagent.service" "$node_unit"
        else
            write_podman_unit >"$TEMP_DIR/dagent.service"
            if [[ $PODMAN_MODE == rootless ]]; then
                mkdir -p "$(dirname "$node_unit")"
                install -m 0644 "$TEMP_DIR/dagent.service" "$node_unit"
            else
                as_root install -m 0644 -o root -g root "$TEMP_DIR/dagent.service" "$node_unit"
            fi
        fi
    fi
    save_configs
    if [[ $ROLE != node ]]; then
        set_admin_password as_user dcompose /usr/local/bin/dcompose --config "$SERVER_CONFIG" --set-password --username "$ADMIN_USER"
    fi
    as_root systemctl daemon-reload
    if [[ $ROLE != server ]]; then
        if [[ $ENGINE == podman && $PODMAN_MODE == rootless ]]; then
            as_root loginctl enable-linger "$NODE_USER"
            systemctl --user daemon-reload
            systemctl --user enable --now podman.socket dagent.service
            systemctl --user enable podman-restart.service
        else
            if [[ $ENGINE == podman ]]; then
                as_root systemctl enable --now podman.socket
                as_root systemctl enable podman-restart.service
            fi
            as_root systemctl enable --now dagent.service
        fi
    fi
    if [[ $ROLE != node ]]; then
        as_root systemctl enable --now dcompose.service
    fi
    printf '\n'
    if [[ $ROLE != node ]]; then
        printf 'Server config: %s\nLogs: sudo journalctl -u dcompose -f\n' "$SERVER_CONFIG"
    fi
    if [[ $ROLE != server ]]; then
        printf 'Node config: %s\n' "$NODE_CONFIG"
        if [[ $ENGINE == podman && $PODMAN_MODE == rootless ]]; then
            printf 'Node logs: journalctl --user -u dagent -f\n'
        else
            printf 'Node logs: sudo journalctl -u dagent -f\n'
        fi
    fi
}

main() {
    while [[ $# != 0 ]]; do
        case $1 in
            --version) [[ $# -ge 2 ]] || fail '--version needs a tag.'; VERSION=$2; shift 2 ;;
            -h|--help) printf 'Usage: bash install.sh [--version TAG]\n\nInstall the server, a node, or both using Docker Compose or native systemd services.\nRequires Linux, Bash, curl, sudo/root and an installed container runtime for nodes.\nExisting installations are not overwritten.\n'; return ;;
            *) fail "Unknown option: $1" ;;
        esac
    done
    [[ $(uname -s) == Linux ]] || fail 'This installer supports Linux.'
    case $(uname -m) in
        x86_64|amd64) ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) fail 'Supported architectures: amd64 and arm64.' ;;
    esac
    [[ -z $VERSION || $VERSION =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] || fail 'Use a release tag or image tag without slashes.'
    need curl
    need realpath
    if [[ $EUID != 0 ]]; then
        need sudo
    fi
    [[ -r /dev/tty ]] || fail 'Run the installer in an interactive terminal.'
    INSTALL_UID=${SUDO_UID:-$(id -u)}
    INSTALL_GID=${SUDO_GID:-$(id -g)}
    printf 'DCompose installation\nRuntime packages must already be installed. Existing installations are left untouched.\n\n'
    choose ROLE 'Install (both/server/node)' both both server node
    choose METHOD 'Deployment (compose/native)' compose compose native
    if [[ $METHOD == compose || $ROLE != server ]]; then
        choose ENGINE 'Container runtime (docker/podman)' docker docker podman
        if [[ $ENGINE == podman ]]; then
            choose PODMAN_MODE 'Podman mode (rootless/rootful)' rootless rootless rootful
        fi
    fi
    ask HOST 'Host IP or hostname for browser access and container links' "$(hostname)"
    [[ $HOST != *[[:space:]/]* && $HOST != *$'\r'* ]] || fail 'Use a hostname or IP address without a scheme or path.'
    if [[ $ROLE != node ]]; then
        ask WEB_PORT 'Web port' 8080
        port "$WEB_PORT"
        ask ADMIN_USER 'Administrator username' admin
    fi
    if [[ $ROLE != server ]]; then
        if [[ $ENGINE == podman && $PODMAN_MODE == rootless ]]; then
            STACKS_DIR=$HOME/compose
        fi
        ask STACKS_DIR 'Absolute directory containing your Compose stacks' "$STACKS_DIR"
        STACKS_DIR=$(absolute_path "$STACKS_DIR")
        if [[ $ROLE == node || $METHOD == native ]]; then
            ask NODE_PORT 'Node port' 9077
            port "$NODE_PORT"
        fi
        if [[ $ROLE == node ]]; then
            ask SERVER_SOURCE 'Server IP, CIDR or hostname allowed to reach this node (any allows all)' any
        fi
    fi
    if [[ $ROLE == both ]]; then
        need openssl
    fi
    if [[ $METHOD == compose || $ROLE != server ]]; then
        need docker
        [[ $(docker --version) == Docker\ version* ]] || fail 'Install the Docker CLI, not the podman-docker wrapper.'
        docker compose config --help | grep -q -- --no-env-resolution || fail 'Install Docker Compose 2.35 or later.'
    fi
    if [[ $ENGINE == podman && ( $METHOD == compose || $ROLE != server ) ]]; then
        need podman
        if [[ $PODMAN_MODE == rootless ]]; then
            [[ $EUID != 0 ]] || fail 'Run the installer without sudo for rootless Podman.'
            [[ $(podman --remote=false info --format '{{.Host.Security.Rootless}}') == true ]] || fail 'Select the user who owns the rootless Podman containers.'
        fi
    fi
    if [[ $METHOD == compose ]]; then
        if [[ $ENGINE == docker ]]; then
            docker info >/dev/null
            local endpoint
            if [[ -n ${DOCKER_CONTEXT:-} ]]; then
                endpoint=$(docker context inspect "$DOCKER_CONTEXT" --format '{{.Endpoints.docker.Host}}')
            else
                endpoint=${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}
            fi
            [[ $endpoint == unix://* ]] || fail 'Compose installation requires a local Docker Unix socket.'
            DOCKER_SOCKET=${endpoint#unix://}
        else
            local socket=/run/podman/podman.sock
            if [[ $PODMAN_MODE == rootless ]]; then
                socket=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock
            fi
            ask PODMAN_SOCKET 'Host Podman Unix socket' "$socket"
            PODMAN_SOCKET=$(absolute_path "$PODMAN_SOCKET")
        fi
        VERSION=${VERSION:-master}
        local directory=/opt/dcompose
        if [[ $ROLE != server ]]; then
            directory=$STACKS_DIR/dcompose
        fi
        ask INSTALL_DIR 'Compose installation directory' "$directory"
        INSTALL_DIR=$(absolute_path "$INSTALL_DIR")
    elif [[ $ROLE != server && $ENGINE == docker ]]; then
        as_root docker --host unix:///var/run/docker.sock info >/dev/null
        getent group docker >/dev/null || fail 'The native Docker node needs the docker group.'
    fi
    printf '\nInstall: %s, %s' "$ROLE" "$METHOD"
    if [[ $ROLE != server ]]; then
        printf ', %s node; stacks: %s' "$ENGINE" "$STACKS_DIR"
    fi
    printf '\n'
    local confirm
    choose confirm 'Continue (yes/no)' no yes no
    [[ $confirm == yes ]] || return 0
    TEMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/dcompose-install.XXXXXX")
    trap 'rm -rf -- "$TEMP_DIR"' EXIT
    umask 077
    PAIRED=false
    [[ $ROLE != both ]] || PAIRED=true
    if [[ $METHOD == compose ]]; then
        compose_install
    else
        native_install
    fi
    local address=$HOST
    if [[ $address == *:* && $address != \[*\] ]]; then
        address=[$address]
    fi
    if [[ $ROLE != node ]]; then
        printf '\nSign in at http://%s:%s with the account you created.\n' "$address" "$WEB_PORT"
    fi
    if [[ $ROLE == node ]]; then
        printf '\nIn Nodes > Add node, enter https://%s:%s and the single-use pairing token from the node logs.\nRestrict this port to your DCompose server.\n' "$address" "$NODE_PORT"
    elif [[ $ROLE == both ]]; then
        printf 'The local node is configured.\n'
    fi
    if [[ $ROLE != node ]]; then
        printf 'Use an HTTPS reverse proxy before exposing the web interface over the Internet.\n'
    fi
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
    main "$@"
fi
