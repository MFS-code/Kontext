#!/bin/sh
set -eu

MODE="${KONTEXT_MODE:-task}"
GOAL="${KONTEXT_GOAL:-think about the requested task}"
RUN_NAME="${KONTEXT_RUN_NAME:-run}"
AGENT_NAME="${KONTEXT_AGENT_NAME:-agent}"

if [ "$MODE" = "service" ]; then
  echo "> Echo service ${AGENT_NAME} (${RUN_NAME}) is alive."
  echo "> Goal: ${GOAL}"
  while true; do
    echo "> Heartbeat from ${AGENT_NAME} at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    sleep 15
  done
fi

RESULT="Echo runtime completed goal for ${RUN_NAME}: ${GOAL}"
echo "> Processing goal: ${GOAL}"
jq -n \
  --arg result "$RESULT" \
  --argjson tokensUsed 12 \
  --argjson dollarsUsed 0 \
  '{result: $result, tokensUsed: $tokensUsed, dollarsUsed: $dollarsUsed}' \
  > /dev/termination-log
echo "RESULT: ${RESULT}"
exit 0
