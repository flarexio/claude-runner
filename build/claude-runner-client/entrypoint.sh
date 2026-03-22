#!/bin/sh
set -e

# Write NATS credentials file from secret
if [ -n "$INPUT_NATS_CREDS_CONTENT" ]; then
  echo "$INPUT_NATS_CREDS_CONTENT" > "$INPUT_NATS_CREDS"
fi

# Build args, skipping empty optional values
ARGS="--transport $INPUT_TRANSPORT --prompt $INPUT_PROMPT"

[ -n "$INPUT_REPO" ]       && ARGS="$ARGS --repo $INPUT_REPO"
[ -n "$INPUT_REF" ]        && ARGS="$ARGS --ref $INPUT_REF"
[ -n "$INPUT_NATS_URL" ]   && ARGS="$ARGS --nats-url $INPUT_NATS_URL"
[ -n "$INPUT_NATS_CREDS" ] && ARGS="$ARGS --nats-creds $INPUT_NATS_CREDS"
[ -n "$INPUT_EDGE_ID" ]    && ARGS="$ARGS --edge-id $INPUT_EDGE_ID"
[ -n "$INPUT_ENDPOINT" ]   && ARGS="$ARGS --endpoint $INPUT_ENDPOINT"

exec claude-runner-client $ARGS
