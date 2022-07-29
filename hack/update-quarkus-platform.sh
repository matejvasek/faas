#!/usr/bin/env bash

# This script check whether Quarkus templates use current platform.
# It's done by querying https://code.quarkus.io/api/platform.
# If the platform is out of date this scripts create PR with an update.

set -o errexit
set -o nounset
set -o pipefail

set -o xtrace

LATEST_PLATFORM="$(curl -s "https://code.quarkus.io/api/platforms" | \
  jq -r '.platforms[0].streams[0].releases[0].quarkusCoreVersion')"

echo "$LATEST_PLATFORM"

XPATH='//*[local-name()="quarkus.platform.version"]/text()'

CE_TEMPLATE_PLATFORM="$(xmllint --xpath "$XPATH" templates/quarkus/cloudevents/pom.xml)"
HTTP_TEMPLATE_PLATFORM="$(xmllint --xpath "$XPATH" templates/quarkus/cloudevents/pom.xml)"

echo "$CE_TEMPLATE_PLATFORM"
echo "$HTTP_TEMPLATE_PLATFORM"

if [ "$CE_TEMPLATE_PLATFORM" == "$LATEST_PLATFORM" ] && \
  [ "$HTTP_TEMPLATE_PLATFORM" == "$LATEST_PLATFORM" ]; then
  echo "Everything is up-to-date!"
  exit 0
fi

sed -i -E "s#<quarkus.platform.version>.+</quarkus.platform.version>#<quarkus.platform.version>${LATEST_PLATFORM}</quarkus.platform.version>#g" \
  ./templates/quarkus/cloudevents/pom.xml
sed -i -E "s#<quarkus.platform.version>.+</quarkus.platform.version>#<quarkus.platform.version>${LATEST_PLATFORM}</quarkus.platform.version>#g" \
  ./templates/quarkus/http/pom.xml
make zz_filesystem_generated.go

PR_BRANCH="update-quarkus-platform-${LATEST_PLATFORM}"

git config --global user.email "quarkus-bot@example.com"
git config --global user.name "quarkus-platform-update-bot"
git checkout -b "$PR_BRANCH"
git add ./templates/quarkus/cloudevents/pom.xml ./templates/quarkus/http/pom.xml zz_filesystem_generated.go
git commit -m "chore: update Quarkus platform to $LATEST_PLATFORM"
git push --set-upstream origin "$PR_BRANCH"

curl -v \
  -X POST \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: token ${GITHUB_TOKEN}" \
  "https://api.github.com/repos/${GITHUB_REPOSITORY}/pulls" \
  -d "{\"title\":\"chore: update Quarkus Platform to ${LATEST_PLATFORM}\",\"body\":\"chore: update Quarkus Platform to ${LATEST_PLATFORM}\",\"head\":\"${GITHUB_REPOSITORY_OWNER}:${PR_BRANCH}\",\"base\":\"master\"}"