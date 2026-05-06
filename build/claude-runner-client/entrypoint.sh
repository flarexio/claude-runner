#!/bin/sh
set -e

# Write NATS credentials file from secret
if [ -n "$INPUT_NATS_CREDS_CONTENT" ]; then
  printf '%s' "$INPUT_NATS_CREDS_CONTENT" > "$INPUT_NATS_CREDS"
fi

# Build args, skipping empty optional values
set -- \
  --transport "$INPUT_TRANSPORT" \
  --prompt "$INPUT_PROMPT"

[ -n "$INPUT_REPO" ]       && set -- "$@" --repo "$INPUT_REPO"
[ -n "$INPUT_REF" ]        && set -- "$@" --ref "$INPUT_REF"
[ -n "$INPUT_BASE_REF" ]   && set -- "$@" --base-ref "$INPUT_BASE_REF"
[ -n "$INPUT_EVENT" ]      && set -- "$@" --event "$INPUT_EVENT"
[ -n "$INPUT_PR_NUMBER" ]  && set -- "$@" --pr-number "$INPUT_PR_NUMBER"
[ -n "$INPUT_NATS_URL" ]   && set -- "$@" --nats-url "$INPUT_NATS_URL"
[ -n "$INPUT_NATS_CREDS" ] && set -- "$@" --nats-creds "$INPUT_NATS_CREDS"
[ -n "$INPUT_EDGE_ID" ]    && set -- "$@" --edge-id "$INPUT_EDGE_ID"
[ -n "$INPUT_ENDPOINT" ]   && set -- "$@" --endpoint "$INPUT_ENDPOINT"

exec claude-runner-client "$@"
