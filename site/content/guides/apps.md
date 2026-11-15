# The apps it hosts

lodge shows 3 first-party apps. Each one is a daemon of its own behind
lodge.

| App | What it is | Pages |
| --- | --- | --- |
| Notes | frostnotes: notes as plain markdown files, with sync, and MCP for agents. | [frostyard.org](https://frostyard.org/docs/apps/frostnotes/), [repository](https://github.com/frostyard/frostnotes) |
| Mail | frostmail: mail, contacts, calendars and tasks, kept in a daemon. | [frostyard.org](https://frostyard.org/docs/apps/frostmail/), [repository](https://github.com/frostyard/frostmail) |
| Porter | The assistant: a chat panel beside the app you are in. It reaches the other apps through their MCP endpoints. | [frostyard.org](https://frostyard.org/docs/apps/porter/), [repository](https://github.com/frostyard/porter) |

frostnotes and frostmail also work on their own as desktop apps.
lodge is optional for them.

frostread, the feed reader (app `read`), is retired since 2026-11-15. A
lodge that still has its package lists it until you remove the package
and run `sudo lodgectl reload`.

## How an app arrives

An app comes in a package or a system extension of its own. On a Frostyard
image, the
[system extensions catalog](https://frostyard.org/docs/sysexts/) lists the
app daemons. An app installs:

- its manifest and its web files, under `/usr/share/lodge/apps/<id>/`;
- its daemon, which runs as its own system user and listens only on a Unix
  socket, `/run/lodge/apps/<id>.sock`, that only lodge can reach.

lodge serves each app's web files itself and proxies to the daemon. `sudo lodgectl reload` rereads the manifests, and
`sudo lodgectl apps` shows each app's manifest, state and reason. An app
whose contract major does not match lodge's is disabled; the other apps
keep running.

## Addresses

Each app has an id, such as `notes` or `read`. `https://LODGE/<id>/<route>`
is the canonical address of anything in an app, and it keeps working across
app versions. Notes' routes, for example, are `/notes/<vault id>/<path>`.

## Build a new app

A new app follows the
[app contract](https://github.com/frostyard/lodge/blob/main/docs/specs/app-contract.md).
The
[new-lodge-app skill](https://github.com/frostyard/lodge/blob/main/.agents/skills/new-lodge-app/SKILL.md)
scaffolds one in a new repository: a daemon on lodge's kit, a JSON-RPC API,
a web bundle, and the manifest and units.
