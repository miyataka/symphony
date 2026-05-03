set -eu

if [ -z "${SYMPHONY_REPOSITORY_SSH_URL:-}" ]; then
  echo "SYMPHONY_REPOSITORY_SSH_URL is required" >&2
  exit 1
fi

if [ ! -d .git ]; then
  git clone "$SYMPHONY_REPOSITORY_SSH_URL" .
fi

git fetch origin --prune
base_branch="$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's|^origin/||')"
git checkout -B "$SYMPHONY_BRANCH" "origin/$base_branch"
