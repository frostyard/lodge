#!/bin/sh
# Render the new-lodge-app scaffold into DEST: every file under template/,
# with its .tmpl suffix dropped and its placeholders filled, in file names
# and in contents. SKILL.md says how to use it.
#
#   render.sh DEST ID NAME [USER] [KEY] [MODULE] [LODGE_REF]
#
#   ID         the app's id and URL segment: ^[a-z][a-z0-9]{0,15}$
#   NAME       its name in the rail: 1 to 32 of A-Z a-z 0-9 space . _ -
#   USER       its system user (default frost<ID>): ^[a-z_][a-z0-9_-]{0,31}$
#   KEY        its switcher key, one letter, or "" for none (default "")
#   MODULE     its Go module path (default github.com/frostyard/<USER>)
#   LODGE_REF  the lodge-client release (client/vX.Y.Z, default client/v0.2.0)
#              or the lodge commit its web bundle pins lodge-client at
#
# DEST must not exist, or be empty: render.sh never overwrites a file.
set -eu

usage="usage: render.sh DEST ID NAME [USER] [KEY] [MODULE] [LODGE_REF]"
dest=${1:?$usage}
id=${2:?$usage}
name=${3:?$usage}
user=${4:-frost$id}
key=${5:-}
module=${6:-github.com/frostyard/$user}
ref=${7:-client/v0.2.0}
here=$(cd "$(dirname "$0")" && pwd)

fail() {
  echo "render.sh: $*" >&2
  exit 1
}
matches() { # value pattern: whether value matches the extended regex, whole
  printf '%s\n' "$1" | grep -Eqx -- "$2"
}

matches "$id" '[a-z][a-z0-9]{0,15}' || fail "id $id must match ^[a-z][a-z0-9]{0,15}\$"
case $id in app | device | lodge | mcp) fail "id $id is reserved" ;; esac
matches "$name" '[A-Za-z0-9 ._-]{1,32}' || fail "name \"$name\" must be 1 to 32 of A-Z a-z 0-9 space . _ -"
matches "$user" '[a-z_][a-z0-9_-]{0,31}' || fail "user $user must match ^[a-z_][a-z0-9_-]{0,31}\$"
case $user in root | lodge) fail "user $user may not run an app" ;; esac
[ -z "$key" ] || matches "$key" '[a-z]' || fail "key $key must be one letter, a to z"
matches "$module" '[A-Za-z0-9._/-]+' || fail "module $module is not a Go module path"
matches "$ref" '[0-9a-f]{7,40}|client/v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)' ||
  fail "lodge ref $ref is neither a client release (client/vX.Y.Z) nor a commit"
if [ -e "$dest" ] && [ -n "$(ls -A -- "$dest")" ]; then
  fail "$dest is not empty; render into a new directory"
fi

fill() { # the placeholders, filled; no value holds a sed metacharacter
  sed -e "s|__ID__|$id|g" -e "s|__NAME__|$name|g" -e "s|__USER__|$user|g" \
    -e "s|__KEY__|$key|g" -e "s|__MODULE__|$module|g" -e "s|__LODGE_REF__|$ref|g"
}

mkdir -p -- "$dest"
dest=$(cd -- "$dest" && pwd)
cd "$here/template"
find . -type f | sort | while IFS= read -r src; do
  rel=$(printf '%s\n' "${src#./}" | fill)
  out=$dest/${rel%.tmpl}
  mkdir -p -- "$(dirname -- "$out")"
  if [ -n "$key" ]; then
    fill <"$src" >"$out"
  else
    fill <"$src" | sed '/"key": ""/d' >"$out"
  fi
  if [ -x "$src" ]; then chmod +x "$out"; fi
done
echo "rendered $id ($name, user $user) into $dest"
