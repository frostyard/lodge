# 0022 — lodge embeds a NATS broker for app events

- **Status:** Proposed
- **Date:** 2026-11-15

## Context

- **Apps emit events.** A note is created, a message arrives, a bookmark
  is saved. An agent, or another client outside lodge, may want to know,
  and react without polling.
- **A consumer gets only what it may read.** An event of one app reaches
  another only when lodge says it may. An app's state is its own
  ([app contract, Rules](../specs/app-contract.md#rules)), and an agent
  reaches an app only through its own MCP endpoint and token
  ([ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)).
- **Events are durable and at least once.** A consumer that was away
  catches up, resumes at its own position, and acknowledges what it took.
- **A grant says what a consumer may read.** A principal's grant
  ([ADR-0023](0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md))
  holds items `{cap, sel}`. An app on hearth declares, for each event, a
  topic `<app>.<object>.<verb>`, with `.<selector>` and 1 token per path
  segment when the event carries a selector, and the capability that
  reads it. So a read grant on a path is a topic prefix, and the broker
  can filter on it before it delivers. A consumer allowed 1 event of 100
  receives 1.
- **A stream cannot keep everything.** An app's events pile up while a
  consumer is away. A stream needs a limit, and a consumer that comes back
  after the limit dropped what it had not read must learn that it missed
  something: events are thin, so it can read the current state again
  through the app's API.
- **Only lodge listens on the network,** and it adds no port
  ([ADR-0003](0003-lodge-is-the-only-process-on-the-network.md),
  [ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md),
  frostyard/core
  [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)).
  A second service with a port of its own, a login or a certificate is a
  second thing to expose, harden and reach from the phone.
- **Apps are their own users,** and meet lodge on Unix sockets
  ([ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md)).
  The kernel's `SO_PEERCRED` names an app's daemon without a credential
  it could copy.
- **A reply subject defeats a permission.** With 1 account and subject
  permissions, a client may send
  `$JS.API.CONSUMER.MSG.NEXT.<stream>.<its consumer>` with the reply
  subject `notes.forged`. JetStream delivers the message into
  `notes.forged`, in the same account, and the notes stream stores a
  forged message. A publish with a forged reply does the same. A
  permission on the subjects a client may publish cannot stop it: the
  reply subject is not the subject the request is published to. Only an
  account boundary stops it. A test of the embedded server showed the
  forged message, and `TestRepliesStayInsideTheirAccount` guards the fix.

## Decision

- **lodge embeds `github.com/nats-io/nats-server/v2`,** with JetStream,
  in its own process. Its store is `/var/lib/lodge/jetstream`, and it has
  no listener of its own: `DontListen`, no monitor, no cluster, no
  leafnode, no gateway, no WebSocket port of NATS's.
  - It is **on by default**. The `broker` member of
    `/etc/lodge/config.json` is `{"enabled": false}` to turn it off; a
    file or a member without `enabled` leaves it on. lodge reads it at
    start, and a change takes effect at the next start. Off, lodge makes no
    socket and no store. A broken member, or a broker that cannot start,
    leaves it off with a log line, and the rest of lodge keeps serving.
  - It installs no signal handler (`NoSigs`), because lodge owns SIGHUP
    and SIGTERM;
    it caps a payload at 64 KiB and its connections at 1,024; its log
    goes to lodge's `slog`.
- **2 doors, both lodge's.** A connection reaches the broker only
  through one of them:
  - `/run/lodge/nats.sock`, owner `lodge`, group `lodge-apps`, mode 0660,
    for app daemons. A connection is admitted only when its peer uid is an
    enabled app's manifest `user`; any other is closed before lodge reads
    from it, and the refusal is logged at most once a minute per uid.
  - `/nats` on lodge's `lodge` node: a WebSocket, `GET` only, with a
    named tailnet peer, no `Origin`, no second listener. A browser is
    refused: its token would have to live in a page. A message over 64 KiB
    ends the connection, as on the server itself: the WebSocket's read
    limit is set after the wrapper that turns it off.
- **lodge reads 1 protocol line, never a message.** On either door it
  reads the connection's `CONNECT` line (at most 8 KiB), decides the
  identity, removes `auth_token`, `user`, `pass`, `jwt`, `nkey` and
  `sig`, and writes the line again to an in-process connection with a
  one-time secret of 32 random bytes in `auth_token`. The server's
  authentication callback uses the secret up, and registers the identity
  lodge chose. lodge never parses a published message, and the server's
  authentication reads only lodge's index.
  - **The line's options must be 1 JSON object.** Anything else, `null`
    included, is refused on both doors before lodge names the
    connection: lodge writes the secret into those options, and a map
    that is nil has none. A panic in a connection's goroutine would end
    lodge's whole process.
- **An account per app and per client.** The server's global account is
  the hub: lodge's own client, which makes streams and consumers, and the
  stream that holds each app's messages. Where a connection may publish
  and subscribe is decided by its account, so a reply never leaves the
  account that sent the request.
  - App `ID`: account `app_ID`, which imports `ID.>` from the hub's export
    of it. Its user may publish `ID.>` and subscribe `_INBOX.>`.
  - Client `NAME`: account `client_NAME`, which imports only its own
    consumers' pull requests, `$JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>`,
    and its own acks, `$JS.ACK.<stream>.<consumer>.>`, from the hub's 2
    exports. Its user may publish exactly those subjects, and subscribe
    `_INBOX.NAME.>`. With no consumer it may publish nothing.
- **1 stream per app,** named `app_ID`, subjects `ID.>` and `lodge.ID.>`.
  It keeps 7 days or 1 GiB, whichever comes first, and discards the
  oldest. A publish from the app is acknowledged by JetStream, and a
  duplicate message ID within 2 minutes is stored once. Only lodge
  publishes `lodge.ID.>`, its resync events; `lodge` is a reserved app ID,
  so no app's subjects overlap it.
- **1 durable pull consumer per client and subscription,** named
  `<client>-<sub>` (each matching `^[a-z0-9_]{1,32}$`, so the name has no
  ambiguity), acknowledging explicitly, from new messages, with filters
  inside one app's subjects. A pull expires after at most 60 s, below the
  2 minute response threshold of a service export, and takes at most 256
  messages. The consumer's metadata names its client, its subscription
  and the topic it asked for, so the index comes back at lodge's next
  start.
  - **Its filters are the effective read prefixes,** and its own resync
    subject, `lodge.ID.resync.<consumer>`. So its filter list is never
    empty, which JetStream would read as every subject.
  - **A subscription stays on 1 app.** Its name is 1 consumer's, so
    subscribing the same client and subscription name to another app is
    refused, with an error that asks for an unsubscribe first. Subscribing
    it again to the same app updates its filters.
  - **A client's subscriptions run 1 at a time.** A per-client lock holds
    a subscribe and an unsubscribe, so the one-app check and the consumer
    it makes are 1 step: 2 subscribes of the same client and subscription
    leave 1 consumer, not 1 per app. A revoke takes no such lock.
- **Permissions are fixed when a connection opens.** A subscription,
  an unsubscribe and a revoke close the client's connections, so it
  reconnects with the new permissions.
- **A revoke closes first, then deletes.** Revoking a client takes it out
  of lodge's index, closes its connections at once, and only then deletes
  its consumers, concurrently and within 1 s: a consumer's deletion can
  wait for the server's own 5 s API timeout, and a revoked client must
  not stay connected meanwhile.
  - A WebSocket ends now, with no close handshake, so an uncooperative
    peer cannot delay the revoke for its 5 s, and the connections close
    together rather than 1 after another.
  - A deletion that fails is kept in a set the authentication never
    reads, and the client's next revoke tries again. The consumer's
    metadata also brings it back at the next start, where a revoke finds
    it.
  - A subscription of that client waits for the kept consumers to go
    first, and fails rather than create a consumer that a kept ref could
    delete later or orphan on another app.
  - **A revoke never waits for a subscription under way.** It takes no
    per-client lock: it counts an epoch for the client, and a subscription
    that a revoke overtakes sees the new epoch, deletes the consumer it
    made, and fails. So a revoke finishes within its 1 s budget however
    long a subscription's JetStream calls take, and no consumer is left
    that nobody deletes.
  - **A subscription waits out a revoke that is in progress.** A revoke
    counts itself, and fails at once any subscribe of that client that
    starts while it deletes, because the revoke's deletion would take the
    consumer the subscribe would make. A revoke ends within 1 s, so the
    subscribe need only ask again.
- **A client is an OAuth client with scope `events`.** Its access token
  ([ADR-0023](0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md))
  is the `CONNECT` line's `auth_token` on `/nats`. Its broker name is
  `o` and the 16 bytes of its client ID in lower-case base32, which fits
  the name rule and maps back to the client. A token without scope
  `events` is refused. A connection closes when its token expires, and
  each minute lodge checks every open connection's token again, so a
  revoked token ends within a minute.
- **A client subscribes by topic prefix** on `/gateway/events`, with the
  same token: 1 or more tokens, `*` or literal, the first an app's ID.
  lodge computes the **effective read prefixes**: for each item of the
  principal's grant for that app, and each event of the app's manifest
  that the item's capability reads, the event's topic with the item's
  path, as hearth's `events.ReadFilters` does; then each is narrowed to
  the requested prefix, and a prefix that another covers is dropped. A
  request that the grant gives nothing under is refused. lodge reads the
  grant inside the client's lock, and a grant change counts a generation
  of its principal and app before it looks for subscriptions: a
  subscription that read the grant before the count moved computes its
  filters again once it is registered. So a grant change and a
  subscription cannot cross.
- **A grant change refilters in place.** When an approval or a removal
  changes a principal's grant for an app, lodge computes the filters of
  each of that principal's subscriptions on the app again and updates the
  consumer. The events it has not delivered yet are filtered again, so a
  narrowed grant stops them; a grant that gives nothing leaves only the
  resync subject. The client's permissions name its consumer, not its
  filters, so its connections stay open.
  - **A refilter that fails fails closed.** When lodge cannot read the
    grant or update the consumer, it revokes the client in the broker:
    its connections close and its consumers go, and it subscribes again.
    A consumer never keeps filters wider than the grant it was last told.
  - An event delivered before the change and not acknowledged may come
    again: JetStream redelivers by sequence, not by filter. It was
    readable when it was delivered.
- **Revoking a client or a principal ends delivery.** lodge's OAuth hook
  revokes each client in the broker, as above: its connections close and
  its consumers go. Each minute lodge also revokes a broker client whose
  OAuth client is gone or no longer has scope `events`, and refilters
  every subscription, in case a hook was missed or an app's manifest
  changed.
- **A consumer older than its stream's first event gets 1 resync
  event.** When a consumer's stream position is before the stream's
  first message, retention dropped events it never saw. lodge then
  publishes 1 CloudEvents message on the consumer's resync subject:
  `type` `org.frostyard.lodge.resync`, `source` `/lodge`, `subject` the
  app's ID, a UUIDv7 `id`, which is also its `Nats-Msg-Id`, and no data.
  The consumer reads on from the stream's first event, and the resync
  tells it to read the state again through the app's API. lodge sends no
  second one while the first is pending: a consumer's check and publish
  run 1 at a time.
  - JetStream moves a consumer's position only when it serves a pull, and
    after that pull the gap cannot be seen. So lodge checks a client's
    consumers when it connects, before its `CONNECT` line reaches the
    server, and every consumer each minute.
- [specs/lodge-http.md](../specs/lodge-http.md#the-broker) and
  [specs/app-contract.md](../specs/app-contract.md#events) pin it, and
  [design/overview.md](../design/overview.md#events) shows where it fits.

## Consequences

- **No new port, no new service.** The broker rides lodge's existing
  listeners and one more Unix socket, so the tailnet gains nothing to
  accept, no certificate, no login.
- **lodge reads 1 line of NATS's protocol** and nothing else: the stream,
  the consumers, the accounts and the permissions are the server's, and
  apps' messages never pass through lodge's code.
- **A large dependency.** The server adds several megabytes to lodge's
  binary and brings its own API surface; lodge uses a small part of it.
  Its releases join lodge's dependency updates.
  Every app and client account lives in memory and is rebuilt at start
  from the consumers' metadata.
- **A client reconnects after a subscription change,** because its
  permission names its consumers. An app does not: its permission is its
  own subject.
- **A pull expires after at most 60 s,** so an idle client re-asks. That
  is below the 2 minute threshold at which the server drops a service
  import's response mapping, and every pull asks again anyway.
- **An agent reads only what its grant reads.** The filter is the
  grant's, computed by lodge from the app's manifest, and JetStream
  applies it before delivery; a client cannot widen it, because it reaches
  only its own consumer's pull requests and acks.
- **Retention is a limit, not a promise.** A consumer away for more than
  7 days, or more than 1 GiB behind, loses events, and learns it from 1
  resync event. A client that stays connected and keeps pulling while its
  backlog ages out can pass the gap between 2 checks of lodge's, and get
  no resync: the limit is sized so that an agent that reads at all is
  never that far behind.
- **Revoking a client is lodge's:** its connections close first, its
  consumers go within 1 s, and nothing of its is left to read. A deletion
  that cannot finish yet is lodge's to retry, not a consumer's to keep.
- **An app that is disabled stops: its connections close, and no new one
  is admitted.** Its stream stays, so its own messages are there when it
  returns.

## Alternatives considered

- **A separate `nats-server` service beside lodge:** a second process, a
  second configuration, its own port and its own login, and a listener
  on the network that lodge does not own. It violates ADR-0003's single
  process, and the app's uid would no longer name it.
- **NATS's own WebSocket listener:** a second listener with its own
  certificate and its own address, and a second way to reach the tailnet,
  where lodge's `lodge` node already terminates TLS and names the peer.
- **1 account with subject permissions:** the reply-subject finding
  above. A client forges a message into another app's stream with the
  reply of a pull request, and no permission can see it, because the
  reply subject is not the subject it published to.
- **An HTTP or server-sent-events pull API of lodge's own:** lodge would
  hold each consumer's position, its acks and its redelivery, and would
  parse the messages it moves. That is a broker written badly, in the one
  process that must stay small.
- **A filter that the client sends:** a client could ask for any
  subject, and lodge would have to check each one against the grant at
  every change. lodge owns the consumer and computes its filter, so the
  grant is the only input.
- **Resync by a new consumer at the stream's first event:** deleting and
  making the consumer again would move it at once, but a pull or an ack
  in between fails, and its pending acknowledgements go. A message on its
  own subject reaches it in order, with no new consumer.
- **NATS JWT accounts or an auth callout:** operator keys, account
  claims and another issuer to run, for accounts lodge makes in its own
  memory. The one-time secret names an identity lodge already decided,
  and the callback needs no network.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#the-broker) (the broker and [`/gateway/events`](../specs/lodge-http.md#event-subscriptions)), [specs/app-contract.md](../specs/app-contract.md#events), [specs/oauth.md](../specs/oauth.md#the-apps-manifest) (the manifest's events), [design/overview.md](../design/overview.md#events)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md) (only lodge listens), [ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md) (an agent reaches an app through its MCP), [ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md) (the nodes and their listeners), [ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md) (a socket's peer uid as identity), [ADR-0023](0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md) (OAuth clients, scope `events` and grants), [ADR-0024](0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md) (apps on hearth and their manifests)
- Related: [ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md) (an app's own socket, admitted by uid); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md) (first-party apps behind lodge)
