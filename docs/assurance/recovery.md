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
| **Standby takes over** (killed after 5 s of steady load, 8 clients, a new request Bundle each; M4, 2026-10-24) | | |
| Asynchronous (the default; the standby follows, answers do not wait): decisions answered | 1,358 to 1,372 | see the CI log |
| Of them not on the standby (recovery point) | **0, 3 and 0** | see the CI log |
| Synchronous (`-standby-sync 2s`): decisions answered | 1,051 (about 23% fewer) | not run in CI |
| Of them not on the standby (recovery point) | **0** | |
| Standby's hash chain | verifies | verifies |
| Kill to first decision on the new server (recovery time) | **1.35 s**, of which 1 s is the drill waiting | |

What the numbers say:

- Every decision Vaglio answers is on disk first: a crash loses none, and the log is whole after it.
- By default the standby is asynchronous: it holds a request open until the primary has new records, so it is
  usually a few milliseconds behind, but an answer does not wait for it, and losing the primary can lose the decisions
  of those milliseconds (3 of 1,358 in one run). Each such decision was answered to the PAS server, so the PAS server
  has it; what the new primary lacks is Vaglio's record of it.
- With `serve -standby-sync 2s` each answer waits until the standby has written the decision to its disk: a takeover
  then loses no answered decision. It costs throughput (about 23% here) and latency (one round trip to the standby
  per answer). If the standby stops answering, each decision waits the stated 2 s and is then answered anyway, and the
  log records `standby.unconfirmed` (at most once a minute): a stopped standby slows decisions, it never stops them,
  and while it is down the recovery point is the asynchronous one. Over a real network the cost will be higher than
  on one machine; not measured.
- Recovery time here is the machine's: it does not include noticing the failure, deciding to fail over, or moving the
  address the PAS server calls. Those are the plan's runbook.

An earlier run of the drill found the standby falling behind under load (905 of 1,716 decisions missing at the kill):
it synced each copied record on its own and waited a full interval even when behind. It now copies a page of records
with one sync and does not wait while behind. Before 2026-10-19 the standby polled every 200 ms (94 of 2,860 missing
at the kill); it now follows.

Until 2026-10-24 the drill sent the same request again and again, so after the first one there was no new Bundle to
keep or to copy, and it under-measured both the primary's work and the standby's: sent a new Bundle per request, as
real traffic is, the standby of that day fell 1,158 of 1,348 decisions behind, fetching and syncing each Bundle on its
own. It now fetches a page's Bundles eight at a time and syncs them once (above), and the primary keeps Bundles in
pack files synced together (README "Speed"). The drill now sends a new Bundle every time, and `vaglio audit verify`,
which it runs on each copy, checks that every case's request reads back and matches.

## Power cut (2026-10-24)

A `kill -9` loses what the process held, never what the operating system held; a power cut loses what the disk had
not been told to keep. `tools/powercut` (Linux, root; GitHub runs it weekly as "Power cut") tests that: Vaglio runs,
anchoring, on a filesystem over the kernel's dm-log-writes, which records every write and every flush sent to the
disk, while clients send requests, a new Bundle each, and after each answer a mark goes into the record. The record is
then replayed onto a fresh device up to every flush, as if the power had gone just after it, and each state is
mounted (on a snapshot, so the replay goes on), a server started on it, its log and every case's request verified
(`vaglio audit verify`), and a new decision taken; and up to every tenth mark, where every decision answered before
the mark must be there with its request.

| | ext4 | XFS |
|---|---|---|
| Decisions answered, each followed by a mark | 303 | 303 |
| States after a flush checked (each started, verified, decided) | **670**, all passed | **672**, all passed |
| Marks checked: every decision answered before them present | **31 of 31** | **31 of 31** |

Run on an EC2 t3.medium (Ubuntu 24.04, kernel 7.0), 2026-10-24. Before the fixes it found, version 0.1.0 failed at the
sixth flush: the first start writes the accounts file by renaming a copy into place, the copy was never synced, and
after a power cut there the file was empty and the server would not start. Fixed: every state file Vaglio keeps is
now written through one function that syncs the copy, renames it and syncs the directory (`fsx.WriteFile`: accounts,
the anchor's keys, the known-browser key, checkpoints' witness proofs, cursors, kept policy versions, certificates).
Also from the test's review of what a power cut can leave: a last log record cut off mid-write is set aside on the
next start (kept in a file beside the log, and said in the log), never a reason to refuse to start, since no answer
waits on a record before its whole line is synced; the anchor signs checkpoints over synced records only, so a power
cut cannot leave a checkpoint over records the disk never kept; and a checkpoint file's torn last line is cut off.

What it does not show: a disk that acknowledges a flush and then loses it (some consumer drives, some virtual disks
configured to ignore flushes) defeats this and any other software; it is the disk's promise that is tested here,
taken at its word.
