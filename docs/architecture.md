# Local process supervision

Kiu currently implements one slot per OS user and host. No permanent daemon is
needed. Each invocation starts a supervisor from the same executable:

```text
client (terminal process group)
  | control pipe: signals, EOF on client death
supervisor (separate process group, owns flock)
  | startup gate: one byte after reservation commit
worker (new process group; exec replaces it with the user's command)
  + managed descendants in the same process group
```

## Admission protocol

1. The supervisor registers its waiting task under `state.lock`, then opens the
   stable `slot.lock` inode and attempts a nonblocking exclusive flock. Contenders retry every 50 ms, with cancellation support.
2. With that lock held, it takes `state.lock` and checks `active.json`. A record for a different kernel
   boot is obsolete. For the current boot, `kill(-pgid, 0)` must report ESRCH
   before its reservation can be removed. Other errors fail closed.
3. It starts a same-binary worker in a new Unix process group. The worker cannot
   execute user code until its startup pipe delivers a commit byte. EOF means exit.
4. It atomically replaces `active.json` with `{schema_version, pgid, boot_id, task}`.
   The temporary file is synced before rename. The file contains no argv or env.
5. It commits startup by writing the gate byte. The worker closes the gate and
   calls exec with the original argument boundaries, working directory and env,
   adding `KIU_EXECUTION_CONTEXT=1` to detect nested invocations.
6. The supervisor reaps its direct child and waits until the entire process group
   is gone. It removes the reservation while still holding the lock, then closes
   the lock descriptor.

Metadata and lock transitions are serialized by the same flock. Metadata alone
does not establish ownership; a reservation supplements the lock after a crash.
Lock files are never deleted. State directories must be private and owned by the
current user. Lock files must be private regular files; symlinks are rejected.
Only local filesystems are in scope. No ordering guarantee follows from flock.

## Failure windows

| Failure | Result |
| --- | --- |
| Client dies while waiting | Control pipe EOF cancels acquisition; no user command starts. |
| Client dies while running | EOF initiates SIGTERM and the five-second grace policy. |
| Supervisor dies before reservation commit | Gate EOF prevents execution; the lock is released. |
| Supervisor dies after reservation but before gate commit | Worker exits on EOF; the next admission removes the dead reservation. |
| Supervisor dies after gate commit | Work may continue; the reservation keeps later invocations waiting until its group disappears. |
| Direct child dies | Supervisor reaps it; remaining group members retain capacity. A signal death also initiates group cancellation. |
| Host reboots | Processes are gone; a different boot ID makes the old reservation safe to remove. |
| Record is malformed or inaccessible | Admission errors with the record path; it never guesses that capacity is free. |

The protocol prioritizes safety over liveness. It never clears a reservation merely
because a supervisor PID is missing, a timeout elapsed, or the leader PID exited.
A process group can outlive its leader. In crash recovery, a reused PGID within
the same boot can conservatively delay admission; recovery never signals that
possibly unrelated group. Normal completion removes metadata immediately to
avoid this ambiguity in the usual path.

The file sync is for orderly reservation publication, not power-loss durability.
A host power loss also ends managed processes. Interrupted temporary files contain
no command data and do not count as reservations.

## Implementation boundaries

The internal helper dispatch and control pipes are an implementation protocol,
not an authentication boundary against another process running as the same user.
The hidden helper name is reserved; `kiu run -- <name>` resolves command-name
conflicts normally. Go test binaries dispatch the same helpers in TestMain so
process-level tests exercise real re-execution rather than a mocked supervisor.

Old prototype binaries that only honor `slot.lock` do not understand crash
reservations. Do not run those binaries alongside this protocol. This is an
unreleased protocol; future compatibility needs an explicit policy.

## Named tasks and coordinated snapshots

`state.lock` serializes registration, active-reservation transitions, task-record
replacement, stale-record pruning and status snapshots. Each task owns a randomly
named `tasks/<id>.lock` until supervision ends; `tasks/<id>.json` contains its
waiting/running metadata. Per-task ownership proves waiting liveness without
relying on PID existence or start-time guesses. Task IDs are never reused.

Admission still depends on `slot.lock` and the crash reservation. A supervisor
first registers as waiting, then acquires the slot. It publishes the named running
record inside `active.json` and updates its task file under `state.lock`, before
opening the worker gate. The reservation's task data remains available after
supervisor SIGKILL even when abandoned per-task files are pruned. A metadata write
failure before gate commit cannot execute user code.

Lock order is slot lock then coordination lock. Operations holding `state.lock`
only **probe** slot/task ownership nonblockingly; they never wait for those locks.
Consequently status remains available while a command runs. Normal cleanup removes
the reservation under coordination only after the group disappears. The supervisor
then removes its task files under coordination. A dead registration's ownership
inode is removed only after a successful exclusive probe; IDs are not reopened or
reused by new registrations.

An empty inherited `KIU_EXECUTION_CONTEXT` must be replaced, not appended as a
second environment key: `syscall.Exec` does not deduplicate environment variables.
The worker now installs exactly one marker. This also permits Kiu's own isolated
subprocess test fixtures to be run under an outer Kiu invocation safely.
