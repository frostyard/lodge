---
title: lodge
description: A tailnet-only web shell for frostyard's first-party apps.
hide:
  - navigation
  - toc
---

<div class="fy-hero" markdown>
<div class="fy-hero__text" markdown>
<p class="fy-eyebrow">lodge</p>

# One address on your tailnet for your apps

<p class="fy-hero__lede">lodge is a tailnet-only web shell for frostyard's first-party apps. One address opens Notes, Mail, Read and the assistant Porter. A quick switcher moves between them, and each app stays loaded in its frame. lodge runs on 1 Linux host, beside the apps' daemons.</p>

[Install lodge →](getting-started/install.md){ .md-button .md-button--primary }
[Configure lodge](guides/configure.md){ .md-button }

</div>
<img class="fy-hero__icon" src="assets/logo.svg" alt="">
</div>

[Install lodge](getting-started/install.md) covers the system extension, the install on another systemd host, and the first start.

## What lodge gives you

<div class="grid cards" markdown>

-   **One address for every app**

    ---

    `LODGE` (`lodge.<tailnet>.ts.net`) opens Notes, Mail, Read and Porter. A quick switcher moves between them. Each app stays loaded in its frame, so a switch keeps your place.

-   **Only lodge is on the network**

    ---

    lodge joins your tailnet as a node of its own. Each app is a daemon on a Unix socket that only lodge can reach. Nothing is on the public internet.

-   **Sign-in by tailnet identity**

    ---

    lodge signs browsers in by their tailnet identity. An allow-list of logins and an allow-list of devices decide who gets in. There are no passwords.

-   **Untrusted bytes stay apart**

    ---

    Untrusted bytes, such as mail parts and attachments, come from a second node, `lodge-content` (`CONTENT`), under a sandbox policy.

-   **One inference layer**

    ---

    lodge routes the apps' model requests to a local model or to the providers you configure. lodge holds every provider credential; the apps hold none.

-   **Scoped access for agents**

    ---

    An agent gets a token for one app, with a scope, at `/mcp/<id>`. An agent on lodge's host, such as Porter, uses a local agent socket.

</div>

## Start here

<div class="grid cards" markdown>

- <span class="fy-index">01</span> **[Install](getting-started/install.md)**

    Install lodge, start it, and allow your login and devices.

- <span class="fy-index">02</span> **[Configure lodge](guides/configure.md)**

    Set agents, inference providers and aliases, and issue tokens.

- <span class="fy-index">03</span> **[The apps it hosts](guides/apps.md)**

    Notes, Mail, Read and Porter, and how an app arrives.

- <span class="fy-index">04</span> **[lodgectl](reference/lodgectl.md)**

    Look up every command of the admin tool.

- <span class="fy-index">05</span> **[Build from source](contributing/build.md)**

    Build lodge and this site yourself.

</div>

!!! note "Pre-release"

    lodge has no stable release yet. Notes, Mail, Read and Porter run behind it. The [lodge page on frostyard.org](https://frostyard.org/docs/apps/lodge/) names the latest release.
