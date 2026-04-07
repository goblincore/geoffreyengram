#!/bin/bash
# Pre-hook: run dualmem explore on session start to inject grounded code context.
# Extracts a topic from the user's prompt and feeds it to explore.
# Exits silently if no clear topic is found.

PROMPT=""
if [ -t 0 ]; then
    PROMPT="$1"
else
    PROMPT=$(cat)
fi

if [ -z "$PROMPT" ]; then
    exit 0
fi

# Simple heuristic: take first line, strip conversational prefixes
TOPIC=$(echo "$PROMPT" | head -1 | sed -E "s/^(hey |hi |hello |please |can you |help me |I need to |lets |let's )//i" | cut -c1-120)

# Skip if topic is too short or generic
if [ ${#TOPIC} -lt 10 ]; then
    exit 0
fi

~/go/bin/dualmem explore "$TOPIC" --budget 3000 2>/dev/null
