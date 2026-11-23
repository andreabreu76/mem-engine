---
name: retry-policy
description: Outbound calls retry three times with exponential backoff
metadata:
  type: project
---

Calls to the payment gateway retry three times with exponential backoff, starting at 200 ms.
Retries are paused during the [[deploy-window]].

```text
[[deploy-window]] and [[not-a-link]] inside a fence stay as they are
```
