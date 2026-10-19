# lodge's shell

The shell page lodge serves for `/`, `/<id>` and `/<id>/<route>`
([specs/shell.md](../docs/specs/shell.md)): the rail on wide screens and
the bar on narrow ones, the switcher on Ctrl+\`, one frame per opened app
that stays loaded, the address bar and history, app states, the theme, and
lodge's web manifest and icons. It talks to frames only through the shell
half of [`@frostyard/lodge-client`](../client/README.md), a `link:`
dependency on `../client`.

It is TypeScript with no framework, built by Vite for the base `/lodge/`
into `dist/`, which installs at `/usr/share/lodge/shell/`. Nothing is
inlined, since the shell's policy allows scripts only from `/lodge/`.

Build and test only in the nsl machine, never on the host
([dev/nsl/provision.sh](../dev/nsl/provision.sh)):

- `make shell-check`: Biome, the typecheck and the tests (Vitest with
  happy-dom).
- `make shell-build`: `shell/dist/`.
- `pnpm run icons` (inside the machine) redraws the PNG icons in `public/`
  from the shapes of `public/icon.svg`.

To try it in a browser, build it, then run the development build with two
fake apps: `make e2e-build && dev/e2e.sh`, and open
`http://127.0.0.1:8480/`.
