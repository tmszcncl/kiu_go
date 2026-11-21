# Execution and failure behavior

Kiu currently supplies one shared slot without configuration, optional `--name`
labels and human/JSON status. Configurable capacity, wait timeouts and no-wait mode
are not implemented yet.

## Supported commands

Use Kiu for noninteractive foreground commands, for example `kiu go test ./...`.
The executable and arguments are passed directly, without shell evaluation.
Working directory, environment, piped/file stdin, stdout and stderr are inherited.
Kiu diagnostics use stderr. Its nested-execution marker is the sole environment
addition. Administrative commands such as `kiu version` do not acquire a slot.

The worker is in a separate background terminal process group. Kiu does not provide
PTY emulation or terminal job-control handoff. Interactive shells, password prompts,
editors and full-screen programs are unsupported. Terminal reads fail instead of
stopping silently on SIGTTIN; stdin from a file or pipe works. SIGTTIN and SIGTTOU
are ignored in the worker and inherited by descendants. SIGTTOU is also ignored
in the supervisor, so writes proceed even with `stty tostop`. Suspend/resume is
not supported.

## Cancellation

SIGINT, SIGTERM, SIGHUP and SIGQUIT received by the client are forwarded through a
private control pipe to the supervisor, then to the entire managed process group.
The first signal starts a five-second grace period. A second cancellation, or
expiry of that period, sends SIGKILL to the group. The slot remains reserved until
the direct child has been reaped and the group no longer exists.

If the direct child crashes or is killed independently, its original signal status
is preserved, but remaining descendants receive SIGTERM and the same grace period.
Crash signals such as SIGSEGV and SIGABRT are never broadcast to descendants.

A waiting cancellation exits without starting the command. The usual signal status
is returned: 130 for SIGINT, 143 for SIGTERM, 129 for SIGHUP, and 131 for SIGQUIT.
An active cancellation returns that same convention even if the command handles
the signal and exits successfully. If the client is killed with SIGKILL, it cannot
return a status, but the supervisor sees pipe EOF and initiates SIGTERM cleanup.

If the main command finishes while descendants remain in its group, Kiu reports
that it is waiting for those processes. It does not silently release capacity or
impose an execution timeout. Cancellation still works during this wait.

## Forced supervisor death

SIGKILL of the supervisor cannot be caught. The client returns 137, but the command
may continue running. Its durable reservation remains and later Kiu commands wait
until the group disappears. Afterward they recover automatically. Kiu does not
automatically send signals based solely on abandoned metadata, because a PGID can
be reused. A hung orphan needs manual investigation and termination of verified
processes. A reused PGID after a crash may conservatively block admission for the
unrelated group's lifetime. Do not delete an active reservation to bypass waiting.

This policy preserves admission safety without claiming automatic termination
after every possible process failure. It is deliberately less convenient than
admitting another heavy command while the old one may still be running.

## Boundaries

- Processes that deliberately change session or process group can escape Kiu.
- A Docker client process group is not the container's process group. Stopping the
  local client does not prove container work stopped. A controlled macOS test
  confirmed that container work continued after cancellation released the host
  slot and admitted the next command. Verify and stop that specific container job
  before retrying. See [manual validation](validation.md).
- Detached services remain outside the contract. `docker compose up -d` only wraps
  the launch command, not the service lifetime.
- Unix zombies can keep a group visible until they are reaped. Linux containers
  must use a reaping init, such as Docker `--init`. Kiu does not free capacity based
  on a racy process-table scan. An uninterruptible process can also delay cleanup.
- Stopping or killing both supervisor and client does not stop the command by
  itself; the reservation still protects the pool while the group exists.
- Independent users, hosts and VMs have separate pools. Commands outside Kiu bypass
  its cooperative admission control.
- A state directory belongs to one PID namespace. Never share it between a host
  and a container or between containers with different PID namespaces.

## Exit codes

| Situation | Status |
| --- | --- |
| Normal command exit | Original child exit code |
| Command or cancellation signal | 128 + signal number |
| Invalid CLI arguments or nested execution | 2 |
| Internal failure, including invalid state | 1 |
| Executable cannot start | 126 |
| Executable missing | 127 |

These overlap with possible child statuses. A status alone is not a unique error
category; diagnostics provide the context.

## Validation

The tests start independent clients, supervisors and commands in isolated state
directories, with bounded deadlines. They exercise argument boundaries, standard
streams, exit statuses, waiting, separate working directories, signal cancellation,
the grace period, forced client/supervisor/child death, gated startup, lingering
descendants, corrupt reservations and unsafe state-file permissions.

Local validation: macOS arm64; Linux arm64 in a Docker VM using a container with
`--init`. The race detector complements these interprocess tests. Targeted local
Flutter and Docker/Rails tests also passed shared-slot checks in both directions
on macOS arm64; a controlled Docker cancellation exposed the container-lifetime
limitation described above. See [manual validation](validation.md). CI, native
amd64, full Rails CI, Flutter builds/device tests and equivalent Linux real-workflow
acceptance remain pending.

## Status JSON schema, version 1

`kiu status --json` writes one JSON object to stdout; diagnostics remain on stderr.
It does not consume an execution slot and is allowed inside a wrapped command.
Top-level fields:

| Field | Meaning |
| --- | --- |
| `schema_version` | Integer `1`. |
| `host` | Local hostname. |
| `capacity` | Integer `1` in this version. |
| `slot_occupied` | A live group reservation or an owned slot lock was observed. |
| `running_count`, `waiting_count` | Lengths of the corresponding arrays. |
| `running`, `waiting` | Task arrays; empty arrays are `[]`, never `null`. |

A known task has `details_available: true`, `schema_version`, a full random `id`, `state` (`running` or `waiting`),
`name`, `executable` (basename only), `cwd`, `client_pid`, `supervisor_pid` and
`submitted_at` (UTC RFC3339 timestamp). Running tasks also have `child_pid`,
`pgid` and `started_at`. IDs are not PIDs; the text view abbreviates IDs for display.
Labels are printable UTF-8, up to 512 bytes. The default label is the project
working directory basename plus executable basename. Full argv and environment
are excluded. A user-provided label or directory can still reveal private data.
Terminal rendering strips non-printable/format characters from paths and hostnames.
Invalid UTF-8 in filesystem names is replaced for JSON/display only; child execution
retains its actual working directory.

For a live reservation created by an older binary, `details_available` is false:
`id`, `executable` and `cwd` are empty, PID fields/timestamps may be zero and
`started_at` is absent. The display name explicitly says details are unavailable.
During startup, `slot_occupied` may be true before any command has started; the
registered job remains waiting until its process group is reserved. Do not infer
an execution start or manufacture another running task from that field alone.

The snapshot is coordinated against registration, reservation publication and
normal cleanup. Processes can die immediately afterward. Lists are ordered by
submission time for display only, without FIFO or priority guarantees. A waiter's
initial stderr/Herdr explanation is one snapshot and is not continuously redrawn;
use status again when fresh details matter. The waiting count includes that waiter.
Status has a bounded five-second coordination-lock wait; failure returns status 1.
Older binaries do not register waiting tasks, so waiting counts only include
invocations implementing this metadata protocol. Upgrade participating binaries
for a complete view.
Older binaries also do not take the metadata coordination lock, so mixed-version
snapshots have weaker transition consistency. The execution slot remains guarded;
upgrade the participating binaries before relying on full queue visibility.

Waiting registrations are live only while their supervisor holds a per-task file
lock. Missing supervisor PIDs or PID reuse do not determine liveness. New job
registration and status queries prune abandoned records under `state.lock`.
Foreign filenames in the task directory are ignored. Malformed recognized live
records return an error rather than fabricated details. Abandoned private atomic
write temporaries are pruned under coordination as well.
Running metadata survives supervisor death inside the active reservation and is
reported while the managed group still exists.

Same-boot PGID reuse after a supervisor crash can conservatively show the old
job name for an unrelated group, just as it can conservatively delay admission.
Status is not proof that a recorded PID/PGID still identifies the original process.

## Recent results

Bare `kiu` shows the last 100 recorded results, newest first. Each result stores
the task ID, label, directory, submission time, optional start time, finish time,
exit code and outcome. A cancelled waiter has no start time. Command arguments,
environment and output are excluded. Results are stored in the user's private
state directory alongside the live metadata. `kiu status` and `kiu status --json`
continue to report current activity.

A supervisor records results after it finishes handling the job. If it is killed
before it can write a result, the job can be absent from history. A failed
history write is reported on stderr and preserves the command's exit status.
