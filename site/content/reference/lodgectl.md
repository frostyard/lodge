# lodgectl

`lodgectl` manages lodge over its control socket,
`/run/lodge/control.sock`. The socket accepts only root and lodge's own
user, so run it as `sudo lodgectl COMMAND`. `version` and `provider login` do not
use the socket: `provider login` runs on another computer, with no running
lodge.

```text
lodgectl [--root DIR] [--json] COMMAND
```

- `--json` prints the answer as JSON.
- `--root DIR` (or `LODGE_ROOT`) finds the socket under `DIR`, for the
  development build.

## Commands

| Command | Does |
| --- | --- |
| `status` | lodge's nodes (with a login URL while a node needs one), apps, sessions, providers and agent socket |
| `apps` | each app's manifest, state and reason |
| `reload` | rereads the manifests and the configuration |
| `logins` | lists the allowed logins |
| `nodes` | lists the allowed nodes |
| `allow login LOGIN` | allows a tailnet login |
| `allow node NODE` | allows a device: its ID, or a name that matches exactly 1 peer |
| `disallow login LOGIN` | removes a login and closes its sessions |
| `disallow node NODE` | removes a device and closes its sessions |
| `tokens` | lists the MCP tokens, never their secrets |
| `token issue NAME --app ID --scope SCOPE [--agent USER]` | prints a new token on stdout, once; with `--agent`, the token works only on the agent socket, from `USER` |
| `token revoke NAME` | revokes a token and closes its streams |
| `sessions` | lists the live browser sessions |
| `session end ID` | closes a session |
| `usage [--since YYYY-MM-DD]` | inference use per app and alias |
| `provider key NAME` | sets an inference provider's key from stdin; an empty input removes it |
| `provider login NAME [--client-id ID] [--email EMAIL] [--port N] [--out FILE]` | on a computer with a browser: signs a `codex` provider in to your ChatGPT plan and writes its login file |
| `provider login NAME --copilot [--out FILE]` | on any computer: signs in to GitHub for a `copilot` provider and writes the GitHub token's file |
| `provider import NAME` | on lodge's host: imports a `codex` login file from stdin |
| `provider models NAME` | lists the models a provider offers |
| `version` | prints `lodgectl`'s version |

The full contract of each command is in
[the control socket and lodgectl](https://github.com/frostyard/lodge/blob/main/docs/specs/lodge-http.md#the-control-socket-and-lodgectl)
and the inference spec's
[lodgectl](https://github.com/frostyard/lodge/blob/main/docs/specs/inference.md#lodgectl)
section.
