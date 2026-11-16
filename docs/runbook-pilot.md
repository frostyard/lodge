# Runbook: the pilot of apps on hearth

This runbook checks the pilot of frostyard/core
[ADR-0085](https://github.com/frostyard/core/blob/main/docs/adr/0085-build-new-first-party-apps-on-hearth-with-lodge-as-their-hub.md):
frostmarks, the first app on [frostyard/hearth](https://github.com/frostyard/hearth),
behind lodge as its hub. The pilot has 7 exit criteria, in core's
[plan 0002](https://github.com/frostyard/core/blob/main/docs/plans/0002-org-portfolio-roadmap.md)
(the incubating repositories). Each one is a line below, L1 to L7, in the
same order. Each line has numbered steps, and each step has its expected
result. The run ends with the [record](#record) of each line as pass or
fail. It is Phase 10 of lodge's
[roadmap](plans/0001-roadmap.md#phase-10--the-pilot-run).

The parts that the run uses:

- lodge's OAuth server, consent and grants pages
  ([oauth.md](specs/oauth.md),
  [ADR-0023](adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md));
- the MCP gateway, `/gateway/mcp`
  ([lodge-http.md](specs/lodge-http.md#the-mcp-gateway),
  [ADR-0024](adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md));
- the broker and the event subscriptions, `/gateway/events`
  ([lodge-http.md](specs/lodge-http.md#event-subscriptions),
  [ADR-0022](adr/0022-lodge-embeds-a-nats-broker-for-app-events.md));
- frostmarks, lodge's app `marks`
  ([its README](https://github.com/frostyard/frostmarks/blob/main/README.md));
- Pi Pocket's `mcp` module, which holds the agent's gateway entry, and
  pi-pocket-team's events bridge, which wakes the agent
  ([the bridge](https://github.com/bketelsen/pi-pocket-team/blob/main/docs/modules/events-bridge.md),
  [its ADR-0004](https://github.com/bketelsen/pi-pocket-team/blob/main/docs/adr/0004-agents-follow-lodge-events-through-a-bridge.md)).

## Who does what

- **The owner** runs every step in a browser on a tailnet device that
  lodge allows, signed in with a login on lodge's admin list. `LODGE` below
  is lodge's name, `lodge.<tailnet>.ts.net`.
- **Chani** is the owner's personal agent in Pi Pocket. The owner tells
  Chani what to do in Chani's chat. Chani's lodge gateway entry is
  `chani-lodge`, and her principal is `chani`.
- L3, L6 and L7 need no person and no agent's data: anyone can run them,
  an agent too.

## Before the run

Check each item. A missing item is not a failed line: fix it first.

1. lodge v0.5.0 or later and frostmarks v0.1.0 or later run on lodge's
   host ([packaging/README.md](../packaging/README.md#update) records the
   release of lodge that the host runs).
   Expected: `https://LODGE` shows Bookmarks in the rail. On lodge's host,
   `lodgectl apps` lists `marks` with version `0.1.0` or later and state
   `up`.
2. The owner's login is a lodge admin.
   Expected: `https://LODGE/oauth/grants` opens and lists the principals.
   A refusal names the step (`lodgectl allow admin LOGIN`).
3. Chani's gateway entry is on and signed in.
   Expected: Pi Pocket > Menu > MCP shows `chani-lodge` on and signed in,
   and `https://LODGE/oauth/grants` lists principal `chani` with client
   `Chani`, scope `mcp`.
4. The events bridge is on.
   Expected: Pi Pocket > Menu > Extensions shows `events-bridge.ts` on.
5. The bookmarklet of frostmarks is in the browser.
   Expected: on any page, it opens `https://LODGE/marks/save?...` in a new
   window. The bookmarklet is in frostmarks' web UI, under
   **Bookmarklet**.
6. 2 folders exist in frostmarks.
   Open `https://LODGE/marks`, and make the folders `/pilot` and `/other`.
   Expected: both folders show in the folder list.

## L1: the agent gets access through the consent page

Done when: the owner tells the agent to get bookmark access; lodge's
consent page shows the requested scopes; the owner narrows and approves;
the agent's bookmark tools appear in its 1 gateway entry with no
configuration change.

1. Tell Chani: "List the tools of your `chani-lodge` MCP server."
   Expected: 1 tool, `request_access`. Its description lists the app
   `marks` (Bookmarks), with the capabilities `marks:read` and
   `marks:write` and the selector `folder`.
2. Tell Chani: "Ask lodge for bookmark access with `request_access`: app
   `marks`, `marks:read` and `marks:write`, each on folder `/`. Give me the
   link."
   Expected: Chani gives a link `https://LODGE/oauth/consent/<id>`.
3. Open the link.
   Expected: lodge's consent page shows client `Chani`, principal `chani`,
   app Bookmarks (`marks`), and 2 items, each checked: `marks:read` with
   folder `/`, and `marks:write` with folder `/`.
4. Narrow: change the folder of both items to `/pilot`. Select
   **Approve**.
   Expected: lodge opens the grants page. Principal `chani` has a grant on
   `marks` with 2 items, `marks:read` on `/pilot` and `marks:write` on
   `/pilot`.
5. In the same chat, tell Chani: "List the tools of `chani-lodge` again."
   Expected: `request_access` and 11 tools named `marks_<tool>`, such as
   `marks_list_bookmarks`, `marks_search_bookmarks` and
   `marks_update_bookmark`. Nobody changed Pi Pocket's `mcp.json`, and
   Chani did not sign in again: lodge sent
   `notifications/tools/list_changed` on her open session.
6. Tell Chani: "List the bookmarks in `/pilot`, then in `/other`."
   Expected: the list of `/pilot` works (no bookmark yet). The list of
   `/other` is a tool error,
   `permission denied: needs marks:read on folder=/other`: the grant does
   not read that folder.

## L2: a saved bookmark wakes the agent, and the agent tags it

Done when: a bookmark saved from the bookmarklet reaches the agent as an
event; with a "now" subscription it wakes the agent, and the agent tags the
bookmark.

1. Tell Chani: "Follow new bookmarks now: `events_subscribe` with topic
   prefix `marks.bookmark` and mode `now`. When a new bookmark comes in
   `/pilot`, add the tag `chani` to it. Keep this rule where your events
   session finds it."
   Expected: the first call shows a sign-in card in Chani's chat.
2. Sign the bridge in, as the card says: open its lodge link, check the
   consent page, select **Approve**, and paste the
   `http://127.0.0.1:8792/...` address from the browser's address bar into
   the card. That address does not load: this is correct.
   Expected: the consent page shows client `chani`, principal `chani` and
   the scope `events` only. After the paste, Chani's call succeeds, and
   `events_list` shows 1 subscription on `marks.bookmark`, mode `now`.
3. Open any web page, and save it with the bookmarklet into folder
   `/pilot`.
   Expected: the save page shows the page's URL and title, and saves the
   bookmark. `https://LODGE/marks` lists it in `/pilot`.
4. Wait 3 minutes. The bridge collects events for 60 seconds before it
   wakes the agent.
   Expected: Pi Pocket's session list shows the session `Chani events` in
   Chani's home. It holds 1 message with 1 line for the event: its time,
   the type `org.frostyard.marks.bookmark.created`, the bookmark's ID as
   subject, the selector `/pilot`, the author and the event ID. The owner's
   chat with Chani gets no message.
5. Look at the bookmark in `https://LODGE/marks`.
   Expected: it has the tag `chani`. Chani's own change makes an event with
   author `chani`; the bridge drops it, so it wakes nobody.

If step 4 passes and step 5 does not, the events session did not find the
rule. Open the session `Chani events`, give the rule of step 1 there, save 1
more bookmark into `/pilot`, and repeat steps 4 and 5. Record that in the
notes of L2.

Delivery is at least once. After a crash or a restart of Pi Pocket, a wake
can carry an event again, marked `repeat=maybe`, and the agent checks its
ID. A repeat is not a failure. A digest subscription (mode `digest`) waits
for 08:00 America/New_York or for `events_pending`; a replay of
`events_pending` after a crash gets its stored answer only within the
bridge's bound (its newest 100 answers, older ones for 8 days). This run
uses mode `now` only.

## L3: a grant on 1 folder receives only that folder's events

Done when: a grant that reads 1 folder receives only that folder's events
(an automated test).

1. Open GitHub > frostyard/lodge > Actions > CI, and the last run on
   `main`.
   Expected: green. Its tests include `TestOneFolderOfMany` in
   [internal/events/acceptance_test.go](../internal/events/acceptance_test.go):
   of 100 events in 10 folders, a grant on 1 folder receives exactly that
   folder's 10. `TestNarrowing` and `TestRevoke` are beside it.
2. Optional, in a checkout of lodge's `main`:
   `go test ./internal/events -run 'TestOneFolderOfMany|TestNarrowing|TestRevoke' -count=1 -v`.
   Expected: each test passes.
3. The live check: save 1 page with the bookmarklet into folder `/other`.
   Wait 3 minutes.
   Expected: the session `Chani events` gets no new message, and Chani's
   `events_list` shows no waiting event. Her grant does not read `/other`,
   so lodge's consumer filters the event out before delivery.

## L4: a revoke ends the tools and the events together

Done when: revoking the grant removes the tools and ends the event
subscription together.

This line revokes the principal `chani`. A principal revoke revokes both
of its clients at once: the gateway entry `Chani` (scope `mcp`) and the
bridge's `chani` (scope `events`), and it deletes the principal's grants.
(**Remove** on 1 grant item narrows the grant instead: the tools go, and
the consumer keeps only its resync subject, but the bridge's subscription
stays open.) [After the run](#after-the-run) restores Chani's access.

1. Open `https://LODGE/oauth/grants`. At principal `chani`, select
   **Revoke**.
   Expected: the page reloads, and `chani` is no longer in the list of
   principals.
2. Tell Chani: "List the bookmarks in `/pilot` through `chani-lodge`."
   Expected: the call fails with an auth error: lodge answers 401 to the
   revoked client's token. No `marks_<tool>` call works. Pi Pocket can ask
   for a new sign-in: do not sign in before [After the run](#after-the-run).
3. Wait 1 minute. Tell Chani: "Run `events_list`."
   Expected: the bridge is paused. lodge closed the bridge's connection,
   deleted its consumer and refused its token, and the bridge deleted its
   events that were not delivered.
4. Save 1 page with the bookmarklet into `/pilot`. Wait 3 minutes.
   Expected: the session `Chani events` gets no new message.

## L5: a second, standard MCP client signs in through the same OAuth flow

Done when: a second, standard MCP client (not pi-pocket) connects through
the same OAuth flow.

The client is MCP Inspector's connection CLI, `mcpdo`, at version
`2.15.0`, the version that `dev/mcp-e2e/package.json` pins for
`make mcp-e2e`. It needs Node.js 22.23.4, 24.22.1, 26.12.1 or later, on a
computer of the tailnet where the owner's browser runs: its sign-in comes
back to a loopback address on that computer.

1. In a terminal, start the sign-in:

   ```sh
   npx -y -p @modelcontextprotocol/inspector@2.15.0 mcpdo connect https://LODGE/gateway/mcp
   ```

   Expected: mcpdo opens lodge's consent page in the browser, or prints its
   address: open it. The page shows a sign-in for a new client, a field
   `principal`, and the scope `mcp`, checked. The client's redirect goes to
   a `127.0.0.1` or `localhost` address.
2. Type `inspector` as the principal. Select **Approve**.
   Expected: the browser goes to mcpdo's loopback address, and mcpdo
   reports the connection. The grants page lists principal `inspector`
   with 1 client, scope `mcp`.
3. Run each of these commands in the same terminal (`mcpdo` below means
   `npx -y -p @modelcontextprotocol/inspector@2.15.0 mcpdo`):

   ```sh
   mcpdo connections/show
   mcpdo tools/list
   ```

   Expected: the server's name is `lodge`, and its capabilities have
   `"listChanged": true`. The only tool is `request_access`.
4. Ask for read access:

   ```sh
   mcpdo tools/call request_access '{"app":"marks","items":[{"cap":"marks:read","sel":{"folder":"/pilot"}}]}'
   ```

   Expected: a result with a `url`, `https://LODGE/oauth/consent/<id>`.
   Open it. The page shows client, principal `inspector`, app Bookmarks
   and 1 item, `marks:read` with folder `/pilot`. Select **Approve**.
5. List and call the tools:

   ```sh
   mcpdo tools/list
   mcpdo tools/call marks_list_bookmarks '{"folder":"/pilot"}'
   mcpdo tools/call marks_update_bookmark '{"id":"<a bookmark ID from the list>","addTags":["inspector"]}'
   ```

   Expected: `tools/list` shows `request_access` and the 4 read tools:
   `marks_get_bookmark`, `marks_list_bookmarks`, `marks_list_folders` and
   `marks_search_bookmarks`. The list call returns the bookmarks of
   `/pilot`. The update call is a tool error that starts with
   `insufficient_scope: principal inspector holds no marks:write for app
   marks`, and the bookmark does not change.
6. Clean up. On `https://LODGE/oauth/grants`, at principal `inspector`,
   select **Revoke**. Then run `mcpdo tools/list`.
   Expected: the call fails. mcpdo reports an auth error, or starts a new
   sign-in: on lodge's consent page, select **Decline**. Then run
   `mcpdo disconnect --clear-auth`. Keep mcpdo for L6.

## L6: frostmarks works with no lodge

Done when: the bookmark manager works with no lodge: its daemon, CLI and
stdio MCP.

Run these steps on any Linux computer with Go 1.27.4 or later, with no
lodge on it. They follow
[frostmarks' README](https://github.com/frostyard/frostmarks/blob/main/README.md#use).

1. Install the 2 commands of the release:

   ```sh
   go install github.com/frostyard/frostmarks/cmd/frostmarksd@v0.1.0
   go install github.com/frostyard/frostmarks/cmd/marksctl@v0.1.0
   ```

   Expected: `frostmarksd` and `marksctl` are in `$(go env GOPATH)/bin`, or
   in the directory that `go env GOBIN` names.
2. Start the daemon, and use the CLI:

   ```sh
   frostmarksd &
   marksctl create-folder /reading
   marksctl create-bookmark -url https://example.com/ -title Example -folder /reading
   marksctl list-bookmarks -recursive
   marksctl search-bookmarks example
   ```

   Expected: each command exits 0. The list and the search each print 1
   line: the bookmark's ID, `/reading`, `https://example.com/` and
   `Example`.
3. Use the stdio MCP server, with the client of L5:

   ```sh
   mcpdo connect marksctl mcp
   mcpdo tools/list
   mcpdo tools/call search_bookmarks '{"query":"example"}'
   ```

   Expected: 11 tools with no prefix, `create_bookmark` to
   `update_bookmark`, and no sign-in. The search returns the bookmark of
   step 2.
4. Clean up: run `mcpdo disconnect` and `mcpdo daemon stop`, stop
   `frostmarksd` (`kill %1`), and remove its state,
   `$XDG_STATE_HOME/frostmarks` (else `~/.local/state/frostmarks`).

## L7: an outside developer builds the example app from hearth's README

Done when: a developer outside frostyard can build a small example app on
the toolkit from its README alone (the toolkit's example app is built only
from the README's steps).

1. Open GitHub > frostyard/hearth > Actions > CI, and the last run on
   `main`.
   Expected: green. hearth's gate builds and tests
   `examples/notes-lite` from a clean checkout, and fails when a code
   block of
   [its README](https://github.com/frostyard/hearth/blob/main/examples/notes-lite/README.md)
   and its file differ.
2. The reader's check: a person or an agent with no frostyard context
   gets only the address of hearth's
   [README](https://github.com/frostyard/hearth/blob/main/README.md). In
   an empty directory, with no checkout of hearth, the reader follows
   `examples/notes-lite/README.md` from **What you need** to its end, with
   hearth `v0.1.0`.
   Expected: `make ci` of the new module passes, and the reader needed
   nothing that the README does not say. Each question or workaround goes
   in the notes of L7.

## After the run

To restore Chani's access after L4:

1. Pi Pocket > Menu > MCP > `chani-lodge` > **Sign in**. On lodge's
   consent page, approve client `Chani` as principal `chani`, scope `mcp`.
2. Tell Chani to ask for her bookmark access again (L1, steps 2 to 4).
3. The next `events_subscribe` of Chani shows the bridge's sign-in card
   again (L2, step 2).

To remove the test data, delete the bookmarks of the run and the folders
`/pilot` and `/other` in `https://LODGE/marks`. A folder must be empty
before its delete.

## Record

Write 1 row for each line: the result, the date, and a note for each
failed step (its number and what happened).

| Line | Done when | Result | Notes |
| --- | --- | --- | --- |
| L1 | Access through the consent page; tools appear with no configuration change | | |
| L2 | A saved bookmark wakes the agent, and it tags the bookmark | | |
| L3 | A grant on 1 folder receives only that folder's events (automated test) | | |
| L4 | A revoke ends the tools and the events together | | |
| L5 | A second, standard MCP client signs in through the same OAuth flow | | |
| L6 | frostmarks works with no lodge | | |
| L7 | An outside developer builds the example app from hearth's README | | |
