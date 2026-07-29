#!/usr/bin/env bash
# Build site/ (Next.js, static export) and publish it to the gh-pages branch,
# served at https://rapidnative.github.io/orchd. gh-pages is generated — never
# hand-edit it; edit site/ on main and re-run this.
#
#   deploy/publish-docs.sh            # build + commit + push
#   deploy/publish-docs.sh --no-push  # build + commit only
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

push=1
[[ "${1:-}" == "--no-push" ]] && push=0

branch=gh-pages
base_path=${SITE_BASE_PATH-/orchd}
src_rev=$(git rev-parse --short HEAD)

echo "==> building site (basePath='${base_path}')"
( cd site && [[ -d node_modules ]] || npm ci )
( cd site && SITE_BASE_PATH="$base_path" npm run build )
[[ -f site/out/index.html ]] || { echo "site/out/index.html missing after build" >&2; exit 1; }

work=$(mktemp -d)/gh-pages
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

# Wipe tracked content so deletions on main propagate to the published site.
find "$work" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +

cp -R site/out/. "$work/"
# Next emits _next/*; Jekyll would drop underscore-prefixed paths.
touch "$work/.nojekyll"

git -C "$work" add -A
if git -C "$work" diff --cached --quiet; then
  echo "gh-pages already up to date with $src_rev"
  exit 0
fi
git -C "$work" commit -qm "Publish site from main@$src_rev"
echo "==> committed $(git -C "$work" rev-parse --short HEAD) on $branch"

if (( push )); then
  git -C "$work" push -u origin "$branch"
  echo "==> pushed -> https://rapidnative.github.io${base_path}/"
else
  echo "==> skipped push (--no-push)"
fi
