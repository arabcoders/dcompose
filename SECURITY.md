# Security

## Reporting a vulnerability

Report privately through [GitHub](https://github.com/arabcoders/dcompose/security/advisories/new), not in a public issue.
Send:

- What an attacker can do
- A working reproduction or proof of concept
- The server and node versions
- Relevant configuration with secrets removed

Report runtime and tool vulnerabilities to their projects, unless DCompose's integration causes the vulnerability or
bypasses authentication or access checks.

## Supported versions

Only the current version is supported.

## Administrator access

DCompose has one local administrator, not separate users with different permissions. Anyone who can sign in or use
the server API key can manage every configured node. A node API token gives control over that node, not just one stack.
Don't give these credentials to anyone you wouldn't trust to administer the hosts.

Registry logins saved in DCompose are shared with every node. Anyone who administers one of those nodes can read them.

## Expected host access

DCompose is a host administration tool. It doesn't sandbox Compose files or shell commands. Authorized users can:

- Deploy arbitrary images and run commands in containers.
- Use privileged containers, host mounts and host namespaces when the runtime permits them.
- Read or change host files through those mounts, including files outside the stacks directory.
- Read stack `.env` files and other secrets accessible through these operations.

With rootful Docker or Podman, this can give full root access to the host. That is expected, not a DCompose privilege
escalation. Running `dagent` in a container or as a separate Unix user doesn't remove the authority of its runtime socket.
Rootless Podman uses the permissions of the user running Podman.

`stacks_dir` tells DCompose where to find stack files. It is not a restriction on what paths a Compose service can mount.

## SSO and sessions

Every accepted OIDC identity signs in as that administrator. Provider roles, groups and claims don't limit what they
can do in DCompose. Restrict who can use the client at your identity provider.

OIDC creates a local session. Disabling the identity or ending its provider session doesn't revoke it. DCompose logout
removes the browser's cookie; it doesn't sign out of the provider or invalidate a copied cookie. Local sessions expire
after 24 hours. Changing the local account credentials or signing key invalidates them. Restart the server after changing
the key externally.

## Reports we close

We close reports that only show:

- Host access or arbitrary command execution through the authorized features listed above.
- First-account creation while no account is configured.
- Administrative operations using valid administrator credentials or an authorized node API token.
- The OIDC and session behavior described above.

If DCompose exposes credentials or accepts them incorrectly, report that. Examples include bypassing login or node
authentication, or a node causing the server to reveal another node's saved credentials. Show how someone without the
required access obtained it, not just what an administrator can do after signing in.

Check scanner and LLM findings yourself. Unreviewed output without a working reproduction and demonstrated impact will
be closed.
