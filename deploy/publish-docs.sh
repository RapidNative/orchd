#!/usr/bin/env bash
# Publish site/ + docs/ to the gh-pages branch (served at
# https://rapidnative.github.io/orchd). Sources live on main; gh-pages is
# generated, never hand-edited.
#
#   deploy/publish-docs.sh            # build + commit + push
#   deploy/publish-docs.sh --no-push  # build + commit only
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

push=1
[[ "${1:-}" == "--no-push" ]] && push=0

branch=gh-pages
work=$(mktemp -d)/gh-pages
src_rev=$(git rev-parse --short HEAD)

cleanup() { git worktree remove --force "$work" 2>/dev/null || true; }
trap cleanup EXIT

if git show-ref --verify --quiet "refs/heads/$branch"; then
  git worktree add "$work" "$branch" >/dev/null
elif git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
  git fetch origin "$branch:$branch" >/dev/null
  git worktree add "$work" "$branch" >/dev/null
else
  git worktree add --detach "$work" >/dev/null
  git -C "$work" checkout --orphan "$branch" >/dev/null
  git -C "$work" rm -rq --cached . 2>/dev/null || true
fi

# Wipe tracked content so deletions on main propagate.
find "$work" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +

cp site/index.html "$work/index.html"

cat >"$work/_config.yml" <<'YAML'
title: orchd
description: One multi-tenant workload orchestrator for tinbase-cloud and RapidNative dev environments
theme: jekyll-theme-primer
baseurl: /orchd
url: https://rapidnative.github.io
exclude: [Gemfile, Gemfile.lock, vendor]
YAML

mkdir -p "$work/docs"

# Jekyll only renders files that have front matter, so prepend it per doc,
# taking the page title from the first markdown H1.
docs_index="$work/docs/index.md"
{
  printf -- '---\nlayout: default\ntitle: Docs\n---\n\n'
  printf '# orchd docs\n\n'
} >"$docs_index"

for f in docs/*.md; do
  base=$(basename "$f")
  title=$(sed -n 's/^# //p' "$f" | head -1)
  [[ -n "$title" ]] || title="${base%.md}"
  {
    printf -- '---\nlayout: default\ntitle: %s\n---\n\n' "$title"
    cat "$f"
  } >"$work/docs/$base"
  printf -- '- [%s](%s)\n' "$title" "${base%.md}.html" >>"$docs_index"
done

printf '\n[Back to overview](../)\n' >>"$docs_index"

git -C "$work" add -A
if git -C "$work" diff --cached --quiet; then
  echo "gh-pages already up to date with $src_rev"
  exit 0
fi
git -C "$work" commit -qm "Publish docs from main@$src_rev"
echo "committed $(git -C "$work" rev-parse --short HEAD) on $branch"

if (( push )); then
  git -C "$work" push -u origin "$branch"
  echo "pushed -> https://rapidnative.github.io/orchd/"
else
  echo "skipped push (--no-push)"
fi
