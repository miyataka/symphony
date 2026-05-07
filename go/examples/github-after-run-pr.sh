set -eu

if [ -z "${SYMPHONY_REPOSITORY:-}" ]; then
  echo "SYMPHONY_REPOSITORY is required" >&2
  exit 1
fi

git rm -f --ignore-unmatch .symphony/prompt.md >/dev/null 2>&1 || true

changes="$(git status --porcelain -- . ':(exclude).symphony' ':(exclude).tmp')"
prompt_cleanup="$(git diff --cached --name-only -- .symphony/prompt.md)"
if [ -z "$changes$prompt_cleanup" ]; then
  exit 0
fi

git add -A -- . ':(exclude).symphony' ':(exclude).tmp'
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
