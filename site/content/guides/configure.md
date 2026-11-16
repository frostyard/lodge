# Configure lodge

lodge keeps 2 kinds of settings. The configuration file holds the agents on
lodge's host, the event broker and the inference layer; it holds no secrets.
The allow-lists, the MCP tokens and the provider credentials live in lodge's
state directory, `/var/lib/lodge/`, and change only through `lodgectl`.
`lodgectl` runs as root: `sudo lodgectl …`.

## The configuration file

The configuration is `/etc/lodge/config.json` (root, `0644`). Start from the
example that the install puts in place:

```sh
sudo cp /usr/share/lodge/config.example.json /etc/lodge/config.json
```

```json
{
  "agents": [],
  "broker": { "enabled": false },
  "inference": {
    "providers": {
      "home": { "type": "openai", "baseURL": "http://MODEL_HOST/v1", "slots": 4, "home": true }
    },
    "aliases": {
      "local": { "provider": "home", "model": "MODEL" },
      "fast": { "provider": "home", "model": "MODEL" },
      "smart": { "provider": "home", "model": "MODEL" }
    }
  }
}
```

- **`agents`** lists up to 16 system user names of agents on lodge's host,
  such as Porter's user. `root` and `lodge` are refused. Each agent's user
  also joins the group `lodge-agents`.
- **`broker`** starts the event broker, which carries each app's events and
  lets a client read them. It is off by default, and a change takes effect
  at the next start of lodge, not on a reload. Its messages are kept in
  `/var/lib/lodge/jetstream/`, and its store grows to 7 days or 1 GiB for
  each app.
- **`inference.providers`** names each model provider. `type` is `openai`
  (any OpenAI-compatible server), `codex` or `copilot`. `slots` is the
  number of requests the provider runs at once. `home: true` marks a
  provider on your own hardware.
- **`inference.aliases`** maps `local`, `fast` and `smart` to a provider and
  a model. The apps ask for an alias, not a model. `local` must map to a
  provider with `"home": true`.
- **`inference.apps`** (optional) limits the aliases an app may use.

lodge reads the file at start. After a change, reload it:

```sh
sudo lodgectl reload
```

lodge refuses a configuration that breaks a rule and keeps serving everything
else; `lodgectl status` shows the reason for the agents and the inference
layer. A `broker` member that breaks a rule, or a broker that cannot start,
shows in lodge's log instead, and the broker stays off. The full rules are in
the inference spec's
[Configuration](https://github.com/frostyard/lodge/blob/main/docs/specs/inference.md#configuration),
the HTTP spec's
[agent socket](https://github.com/frostyard/lodge/blob/main/docs/specs/lodge-http.md#the-agent-socket)
and its
[broker](https://github.com/frostyard/lodge/blob/main/docs/specs/lodge-http.md#the-broker).

## The allow-lists

A browser gets in only when its tailnet login is on the allowed logins and
its device is on the allowed nodes. A change applies to the next request.

```sh
sudo lodgectl logins                        # list the allowed logins
sudo lodgectl allow login alice@example.com
sudo lodgectl nodes                         # list the allowed nodes
sudo lodgectl allow node laptop
sudo lodgectl disallow node laptop          # also closes its sessions
```

## MCP tokens for agents

An agent reaches an app only through that app's MCP endpoint, with a token.
A token works for 1 app. Its scope is a string whose meaning is the app's
own; Notes, for example, uses `vault=<id>[;write]`.

```sh
sudo lodgectl token issue NAME --app ID --scope SCOPE
```

lodge prints the token once, on stdout, and never shows it again. On stderr
it prints how to add it to Claude Code. The token works at
`https://LODGE/mcp/<id>`, from a device on the tailnet.

For an agent on lodge's host, add `--agent USER`. The token then works only
on the agent socket, `/run/lodge/agent.sock`, from that user. The user must
be in `agents` in the configuration.

```sh
sudo lodgectl tokens              # list the tokens (never the secrets)
sudo lodgectl token revoke NAME   # refuse it and close its streams
```

[MCP tokens and scope](https://github.com/frostyard/lodge/blob/main/docs/specs/app-contract.md#mcp-tokens-and-scope)
in the app contract has the details.

## Admins, consent and grants

An MCP client that speaks OAuth signs in to lodge itself, with no token
copied by hand. It finds lodge at `https://LODGE`, registers, and opens a
sign-in link in your browser. lodge shows a **consent page**: the client,
the principal it acts for (such as `chani`), and what it asks for. You
narrow it, approve it, or decline it. An agent that needs more later asks
again, and the request opens the same page.

Only an admin approves. Add your login first:

```sh
sudo lodgectl allow admin alice@example.com
sudo lodgectl admins                          # list the admins
sudo lodgectl disallow admin alice@example.com
```

An admin must also be on the allowed logins, on an allowed device.

`https://LODGE/oauth/grants` is the **grants page**. It lists each
principal, its clients, its grants, when each was last used, and the
requests that wait for you. It revokes a client or a whole principal,
and their tokens stop at once.

The tokens `lodgectl token issue` prints keep working beside OAuth.
[lodge's OAuth server](https://github.com/frostyard/lodge/blob/main/docs/specs/oauth.md)
has the details.

## Provider credentials

The configuration never holds a key. lodge keeps every credential in its
state directory and shows it nowhere.

- **An `openai` provider that needs a key.** Set the key from stdin; an empty
  input removes it:

    ```sh
    sudo lodgectl provider key NAME < FILE
    ```

- **A `codex` provider** uses your ChatGPT plan through Sign in with
  ChatGPT. On a computer with a browser, run
  `lodgectl provider login NAME`, without `sudo`. It writes a login file.
  Copy the file to lodge's host, run
  `sudo lodgectl provider import NAME < FILE`, then delete the file on both
  computers.
- **A `copilot` provider** uses your GitHub Copilot plan. On any computer,
  run `lodgectl provider login NAME --copilot`, and enter the code it shows
  in any browser. It writes a file with a GitHub token. On lodge's host, run
  `sudo lodgectl provider key NAME < FILE`, then delete the file on both
  computers.

`sudo lodgectl provider models NAME` lists the models a provider offers, and
`sudo lodgectl status` shows each provider's availability. The details are
in
[The codex provider](https://github.com/frostyard/lodge/blob/main/docs/specs/inference.md#the-codex-provider)
and
[The copilot provider](https://github.com/frostyard/lodge/blob/main/docs/specs/inference.md#the-copilot-provider).
