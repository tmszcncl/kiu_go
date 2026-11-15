# Kiu

Keep your tasks in line.

Kiu is being built to limit concurrent expensive commands across projects on one
computer. The motivation is several coding agents starting CI and builds at once,
overloading a development machine.

## Current implementation

This is an early development version. It runs commands and preserves argument
boundaries, working directory, environment, standard streams and normal exit codes.
On macOS and Linux, one shared slot admits one command at a time across
directories. Other invocations wait automatically. Waiting can be cancelled.
Nested invocations are rejected to avoid deadlock.

A short-lived supervisor forwards cancellation to the job's Unix process group.
It allows five seconds for cleanup, then sends SIGKILL. A second cancellation
skips the grace period. Killing the client triggers the same cleanup. A durable
reservation prevents new admission if the supervisor is killed while work remains.

This is a development version for **noninteractive build and test commands**.
Interactive terminal programs and descendants that leave the managed process
group are outside its contract. Real Docker/Rails and Flutter acceptance tests
have not been run. See [behavior](docs/behavior.md) for failure policies and
[architecture](docs/architecture.md) for the coordination protocol.

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
go test -race ./... -timeout 120s
go vet ./...
```

Lifecycle tests run locally on macOS arm64 and Linux arm64 in a Docker container
with a reaping init (`--init`). CI and amd64 native validation are pending.
Windows support is planned later.

## License

MIT — see [LICENSE](LICENSE).

## Pool storage

The pool belongs to the current user on this host. On macOS its lock lives in
`~/Library/Application Support/kiu/state/slot.lock`. On Linux it lives in
`$XDG_STATE_HOME/kiu/slot.lock`, or `~/.local/state/kiu/slot.lock` when the XDG
variable is unset or relative. Use a local filesystem. Never remove the lock file
while invocations exist: different inodes would create independent pools.

The directory and lock file are created with modes 0700 and 0600 respectively.
An active reservation stores only a schema version, boot ID and process group ID.
Command arguments and environment are not persisted.
Admission order is best-effort, without a FIFO guarantee.
