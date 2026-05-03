set -eu

if [ -z "${SYMPHONY_REPOSITORY:-}" ]; then
  echo "SYMPHONY_REPOSITORY is required" >&2
  exit 1
fi

if [ -z "$(git status --porcelain)" ]; then
  exit 0
fi

git add -A
git commit -m "${SYMPHONY_ISSUE_IDENTIFIER}: agent update"
git push -u origin "$SYMPHONY_BRANCH"

if gh pr view "$SYMPHONY_BRANCH" --repo "$SYMPHONY_REPOSITORY" >/dev/null 2>&1; then
  exit 0
fi

gh pr create \
  --repo "$SYMPHONY_REPOSITORY" \
  --head "$SYMPHONY_BRANCH" \
  --title "$SYMPHONY_ISSUE_TITLE" \
  --body "Automated work for $SYMPHONY_ISSUE_URL"
