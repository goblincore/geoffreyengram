---
description: End-of-session wrap-up — saves continuity memory for the next session
---

Before ending this session, create a handoff note for the next session.

Review what happened in this session and save a continuity memory by running:

```
dualmem add --type continuity --text "<your summary>" --files "<key files>" --salience 0.8
```

Your summary should include:
1. **What was accomplished** — features built, bugs fixed, decisions made
2. **What's remaining** — unfinished work, next steps, blocked items
3. **Key files touched** — use the --files flag with the most important files from this session
4. **Any gotchas** — things the next session should know about

Keep the summary concise (2-4 sentences). The --files flag is critical — it tells the next session exactly where to start.

Example:
```
dualmem add --type continuity --text "Implemented JWT refresh tokens and added middleware. Remaining: logout endpoint, token revocation list, update API docs. Rate limiter test is flaky — needs investigation." --files "auth.go,middleware.go,jwt.go,rate_limiter_test.go" --salience 0.8
```

If important decisions were made during the session that haven't been saved yet, save those too:
```
dualmem add --type decision --text "<decision and rationale>"
```
