// Package repocheck holds tests that keep repository-wide rules that no
// compiler checks: the documentation rules of AGENTS.md (every relative
// link resolves, every doc is indexed) and the repository boundary (no
// private host names or addresses), and the kit's import boundary
// (kit/ never imports tailscale.com or lodge's other packages).
package repocheck
