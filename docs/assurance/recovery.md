# Recovery, measured

`tools/drill` runs real Vaglio processes in a temporary directory with invented requests, from 8 clients at once, and
kills the server with SIGKILL (no shutdown, nothing flushed). CI runs it on every push (the Linux job's "Recovery drill"
step), so these numbers are re-measured on every change. Below: a run on an Apple M4 laptop, and one on GitHub's
Linux runner, October 2026.

| | M4 laptop | GitHub Linux runner |
|---|---|---|
| **Crash, restart on the same data** | | |
| Decisions answered before the kill | 301 | 301 |
| Of them missing afterwards (recovery point) | **0** | **0** |
| Log hash chain afterwards | verifies | verifies |
| Restart to first decision answered (recovery time) | **0.2 s** | **0.35 s** |
| **Standby takes over** (copying every 200 ms, killed after 5 s of steady load) | | |
| Decisions the primary answered | 2,860 | see the CI log |
| Of them not on the standby (recovery point) | **94** (about one copying interval at this load) | see the CI log |
| Standby's hash chain | verifies | verifies |
| Kill to first decision on the new server (recovery time) | **1.4 s**, of which 1 s is the drill waiting | |

What the numbers say:

- Every decision Vaglio answers is on disk first: a crash loses none, and the log is whole after it.
- The standby is asynchronous. Losing the primary loses the decisions answered since the standby's last copy: at the
  default `-every 2s` under heavy load, up to two seconds' worth. Each such decision was answered to the PAS server, so
  the PAS server has it; what the new primary lacks is Vaglio's record of it. Synchronous replication is not built.
- Recovery time here is the machine's: it does not include noticing the failure, deciding to fail over, or moving the
  address the PAS server calls. Those are the plan's runbook.

An earlier run of the drill found the standby falling behind under load (905 of 1,716 decisions missing at the kill):
it synced each copied record on its own and waited a full interval even when behind. It now copies a page of records
with one sync and does not wait while behind.
