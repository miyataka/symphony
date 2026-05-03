set -eu

if [ -d .git ]; then
  git fetch origin --prune
fi
