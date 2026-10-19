# 0014 — Agents on lodge's host use a local agent socket

- **Status:** Proposed
- **Date:** 2026-10-19

## Context

[ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)
gives each app that serves MCP an endpoint, `https://LODGE/mcp/<id>`, on
lodge's tailnet node. An agent there needs a tailnet identity: lodge checks
that WhoIs names the peer from its own address, then checks the bearer
token. Agents off lodge's host, such as Claude Code on a laptop or a tagged
build machine, reach it that way.

Porter, the assistant, is different
([Porter ADR-0003](https://github.com/frostyard/porter/blob/main/docs/adr/0003-porter-is-a-lodge-app-that-owns-no-data.md)).
It is a lodge app, so it runs on lodge's host, as its own user,
`frostporter`. It owns no data: it reads and writes mail and notes only
through their `/mcp/<id>`, with one scoped token per app. For it to use
the tailnet route:

- it would join the tailnet itself, with tsnet and an auth key of its own,
  as one more node in the owner's tailnet;
- the kit, which Porter is built on, never imports `tailscale.com`, so
  Porter would carry Tailscale in its own code;
- every tool call would leave the host for the tailnet and come back to
  the same machine.

Nothing on the host can name Porter's process the way WhoIs names a node,
except the kernel: a Unix socket's peer uid (`SO_PEERCRED`), which lodge
already trusts for the inference socket
([ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md)).

## Decision

- **lodge listens on `/run/lodge/agent.sock`,** owner `lodge`, group
  `lodge-agents`, mode 0660. An agent's system user joins `lodge-agents`.
- **It admits only the agents lodge's configuration names.** The `agents`
  member of `/etc/lodge/config.json` lists their user names
  (`["frostporter"]`). lodge closes a connection whose peer uid is not one
  of theirs before it reads anything. The group and the list are two
  fences: a user must be in both. The list is reread with the manifests.
- **It serves `/mcp/<id>` and nothing else,** with the same rules as the
  tailnet's `/mcp/<id>`:
  - `Authorization: Bearer` names a live token issued for app `<id>`, or
    the answer is 401;
  - lodge removes `Authorization`, and passes `Lodge-Caller: agent`,
    `Lodge-Token` and `Lodge-Scope`, the scope unread;
  - a request with an `Origin` header is refused, and a disabled app or one
    without `mcp: true` answers 503 or 404;
  - streams are relayed without buffering, and revoking a token closes its
    streams at once;
  - a token's last use is recorded, and each request is logged.

  The peer's user replaces the tailnet's checks: there is no `Host`
  check, no WhoIs and no address check. The log names the agent's user in
  place of a node.
- **A token is bound to where it is used.**
  - `lodgectl token issue NAME --app ID --scope SCOPE --agent USER` issues
    a token for the agent socket, from that user only.
  - A token issued without `--agent` works only on the tailnet, as
    ADR-0006 says.
  - Each refuses the other's route with the same 401, so a token copied
    off the host is no use, and one agent on the host cannot use
    another's.
- **Agents off lodge's host stay on the tailnet,** as ADR-0006 decided:
  Claude Code on the user's laptop, and an agent that runs commands on a
  tagged node of its own, each with its own tokens. The agent socket is
  only for processes on lodge's host that lodge's configuration names.
- **Under `--root`,** the socket is `DIR/run/lodge/agent.sock`, and lodge's
  own uid passes too, as for app sockets, so the development build can
  host an agent run by the developer.
- [specs/lodge-http.md](../specs/lodge-http.md#the-agent-socket) and
  [specs/app-contract.md](../specs/app-contract.md#the-agent-socket) pin
  it. The roadmap's
  [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket)
  builds them.

## Consequences

- **Nothing leaves the host,** and an agent there needs no tsnet, no node
  and no auth key. Porter keeps the kit's boundary, and the owner's tailnet
  gains no node for it.
- Each app's engine still enforces the token's scope, and lodge still
  owns revocation, so the socket widens no agent's reach. A compromised
  agent user can use its own tokens, as on the tailnet, and nothing more.
- **lodge holds one more socket and one more list.** Adding an agent is a
  system user in `lodge-agents`, a line in the configuration, a reload and
  its tokens.
- Binding a token to its route means an agent that moves between the host
  and the tailnet needs a new token. That is the point.
- Apps cannot tell the two routes apart: both send caller `agent` with the
  token's name and scope. That is deliberate, so no app writes a rule per
  route.
- Agents that run commands, such as stomp, stay off lodge's host with
  their own identities, as before.

## Alternatives considered

- **A tagged tailnet node for each agent on the host:** tsnet in the agent,
  an auth key to keep, a node per agent, and traffic out to the tailnet and
  back. It stays the rule for agents elsewhere.
- **A plain HTTP listener on loopback:** any process on the host could
  connect, and lodge could not tell who. Tokens alone would be the fence.
- **The agent calling apps' sockets directly:** an app's socket admits only
  lodge's uid ([ADR-0003](0003-lodge-is-the-only-process-on-the-network.md)),
  and lodge's tokens, revocation and log would be bypassed.
- **A socket per agent:** more units and paths for the same check the peer
  uid already makes.
- **Tokens that work on either route:** simpler, but a token copied from
  the host would work from any tailnet node the policy lets reach lodge.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#the-agent-socket), [specs/app-contract.md](../specs/app-contract.md#the-agent-socket), [design/overview.md](../design/overview.md#agents), [roadmap — Phase 5](../plans/0001-roadmap.md#phase-5--the-assistants-design) and [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket)
- Builds on: [ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md), [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md), [ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md) (a socket's peer uid as identity)
- Related: [ADR-0013](0013-the-companion-opens-beside-the-current-app.md); Porter [ADR-0003](https://github.com/frostyard/porter/blob/main/docs/adr/0003-porter-is-a-lodge-app-that-owns-no-data.md), [ADR-0004](https://github.com/frostyard/porter/blob/main/docs/adr/0004-porter-drafts-and-the-owner-sends.md) (what its tokens may do) and [design/overview.md](https://github.com/frostyard/porter/blob/main/docs/design/overview.md); frostnotes [ADR-0028](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0028-an-agents-scope-can-limit-writes-to-folders.md) (an agent's scope can limit writes to folders) and [plan 0007](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0007-folder-scopes-for-agents.md); frostmail [plan 0012](https://github.com/frostyard/frostmail/blob/main/docs/plans/0012-calendar-tasks-and-contacts-for-agents.md) (calendar and tasks for agents); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
