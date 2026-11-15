# Kiu

Keep your tasks in line.

Kiu is being built to limit concurrent expensive commands across projects on one
computer. The motivation is several coding agents starting CI and builds at once,
overloading a development machine.

## Current implementation

This is an early development version. It runs commands and preserves argument
boundaries, working directory, environment, standard streams and normal exit codes.
On macOS and Linux, one shared file lock admits one command at a time across
directories. Other invocations wait automatically. Waiting can be cancelled.
Nested invocations are rejected to avoid deadlock.

**Active cancellation and crash-safe descendant management are unfinished.**
Killing Kiu can release its slot while a child continues running. This prototype
is not ready to protect capacity during real builds.

```sh
go build -o kiu ./cmd/kiu
./kiu version
./kiu go test ./...
./kiu run -- sh -c 'printf "hello\n"'
```

Arguments after the executable belong to the child. Kiu does not interpret shell
syntax; invoke a shell explicitly when needed. `run`, `config`, `status`, and
`version` are reserved names. Use `kiu run -- status` to execute a program named
`status`. Configuration and status commands are not implemented yet.

Exit codes: child status on normal completion; 2 for CLI usage errors; 126 when an
executable cannot start; 127 when it is missing. On macOS and Linux, a child killed
by a signal returns 128 plus the signal number.

## Development

```sh
go test ./...
go vet ./...
```

Initial platform targets are macOS and Linux. Native Linux lifecycle validation
is still pending. Windows support is planned later.
The module path is temporarily `kiu` until a GitHub repository is selected.
The proposed MIT license awaits the owner's confirmation; no license is granted
by this README.

## Pool storage

The pool belongs to the current user on this host. On macOS its lock lives in
`~/Library/Application Support/kiu/state/slot.lock`. On Linux it lives in
`$XDG_STATE_HOME/kiu/slot.lock`, or `~/.local/state/kiu/slot.lock` when the XDG
variable is unset or relative. Use a local filesystem. Never remove the lock file
while invocations exist: different inodes would create independent pools.

The directory and lock file are created with modes 0700 and 0600 respectively.
Only the lock is stored; command arguments and environment are not persisted.
Admission order is best-effort, without a FIFO guarantee.
