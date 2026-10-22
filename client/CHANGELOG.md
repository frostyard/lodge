# Changelog

Every release of `@frostyard/lodge-client`, newest first
([the spec](../docs/specs/lodge-client.md#the-rpc-subpath)). Dates are the
commits' dates. A release is a lodge tag `client/vX.Y.Z`. Older versions
are in the git tags `client/vX.Y.Z`, each with the code it shipped.

## 0.3.2 (2026-10-22)

- `connectLodgeAPI` rejects with the `RPCError` of a failed `rpc.hello`, so
  the caller gets its `code`, such as a protocol mismatch's 1003, and the
  daemon's message. Before, it rejected with a plain `Error` of the
  message alone, and the code was lost. Other failures keep their `Error`
  and their message.
