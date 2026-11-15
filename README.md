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

## Installation from source

You need Git and Go 1.23 or newer on macOS or Linux. Release archives are not
available yet.

```sh
git clone https://github.com/tmszcncl/kiu_go.git
cd kiu_go
go install ./cmd/kiu
```

By default, Go installs the executable as `$(go env GOPATH)/bin/kiu`. Add that
directory to PATH in your current terminal:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
kiu version
```

The current source build prints `kiu dev`; that is expected. If you set a custom
`GOBIN`, add that directory to PATH instead.

For future terminals, add this line **once** to `~/.zshrc` if you use zsh
(the default shell on macOS), or `~/.bashrc` if you use bash on Linux:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

Open a new terminal, or run `source ~/.zshrc` (zsh) / `source ~/.bashrc` (bash).
Place the PATH line after the shell setup that makes `go` available. Alternatively,
add the literal directory printed by `go env GOPATH` with `/bin` appended; this
avoids running Go on every shell startup.
Restart existing coding-agent sessions so they inherit the updated PATH. You can
now run `kiu` from any project directory; no project-specific setup is required.

To update a clean source checkout, run `git pull --ff-only` and then
`go install ./cmd/kiu` again.

## Usage

```sh
kiu run -- sh -c 'printf "hello\n"'
```

To try the queue, run `kiu sleep 10` in one terminal, then run
`kiu echo "slot acquired"` in another. The second invocation waits until the first
finishes. Both terminals must run as the same OS user on the same host, using
the same state-directory environment (see [pool storage](#pool-storage)).

In your Go application's directory, run tests with `kiu go test ./...`.
For Kiu's own test harness, use the unwrapped commands in [Development](#development).

Arguments after the executable belong to the child. Kiu does not interpret shell
syntax; invoke a shell explicitly when needed. `run`, `config`, `status`, and
`version` are reserved names. Use `kiu run -- status` to execute a program named
`status`. Configuration and status commands are not implemented yet.

Exit codes: child status on normal completion; 2 for CLI usage errors or nested
execution; 126 when an executable cannot start; 127 when it is missing. On macOS and Linux, a child killed
by a signal returns 128 plus the signal number.

## Using Kiu with coding agents

Installing Kiu does not make agents use it automatically. Append the following
section to your project's `AGENTS.md`, or to the instruction file your agent
actually reads (for example `CLAUDE.md`). Keep the project's existing instructions.
Restart agent sessions after changing PATH, and tell existing sessions to reread
updated project instructions.

```md
## Shared capacity for tests and builds

- Check `kiu version` before the first protected command in this session.
  If Kiu is unavailable, report it; do not silently run protected commands directly.
- Prefix each test run, CI script, build, mutation test and large import with `kiu`.
  This includes a single test file or test example. Follow the project's existing
  command, working directory, environment and arguments; add the prefix once.
- Allow queued commands to wait. Do not launch duplicates or bypass Kiu because
  a command is waiting. Do not delete lock files or reservations to unblock it.
  Use a sufficiently long tool timeout, or your agent's background execution and
  wait mechanism. Keep tracking the original invocation until it exits.
- Wrap the outermost test/build entry point once. For example, `kiu bin/ci`
  covers tests called by that script. Do not add Kiu again inside the wrapped
  command; nested invocations inheriting its environment are rejected.
  If a script already wraps its internal commands with Kiu, run that script without an additional outer Kiu.
- Leave Git, searches, file reads, standalone formatting and other lightweight
  commands unwrapped. A CI script that also runs formatters is still wrapped once.
- Do not wrap interactive programs, development servers, watch mode, Rails
  consoles, or `flutter run`. Their long lifetimes would occupy the shared slot.
  Protected commands must not prompt for terminal input; prepare credentials,
  dependencies and target-device selection before starting them.
- Cancel through the original Kiu invocation when necessary. Wait for cleanup;
  do not assume a cancelled Docker client means container work has stopped.
  Check the specific job/container before retrying. Stop only verified leftover
  work belonging to that job, following the project's cleanup instructions.
- Kiu currently has one shared slot. `kiu status`, configuration, labels,
  `--no-wait` and `--wait-timeout` are not implemented; do not invent these options.
```

This is an advisory project policy, not automatic enforcement. It intentionally
queues even small test runs so independent agents follow one predictable rule.
One wrapped command consumes one slot regardless of how many internal test
workers it starts. Commands that bypass Kiu do not participate in the pool.

### Ruby: Rails and RSpec

Use the entry points already provided by your project; do not introduce RSpec
into a Minitest project or assume every Rails project has `bin/ci`.

| Task | Example command |
| --- | --- |
| Project CI script | `kiu bin/ci` |
| Rails / Minitest suite | `kiu bin/rails test` |
| Rails / Minitest file | `kiu bin/rails test test/models/user_test.rb` |
| One Rails / Minitest test by line | `kiu bin/rails test test/models/user_test.rb:42` |
| Rails system tests | `kiu bin/rails test:system` |
| RSpec suite | `kiu bundle exec rspec` |
| RSpec file | `kiu bundle exec rspec spec/models/user_spec.rb` |
| One RSpec example by line | `kiu bundle exec rspec spec/models/user_spec.rb:42` |

If the project uses `bundle exec rails test` instead of `bin/rails test`, use
`kiu bundle exec rails test`. Preserve any existing test filters and options.
For environment assignments in sh, bash or zsh, keep shell syntax outside the command:

```sh
RAILS_ENV=test kiu bundle exec rspec spec/models/user_spec.rb
```

Alternatively, `kiu env RAILS_ENV=test bundle exec rspec spec/models/user_spec.rb` passes the assignment
through the `env` executable. See the [Rails testing guide](https://guides.rubyonrails.org/testing.html)
for test selection and system-test setup.
System tests require the project's browser/driver setup, including its Selenium
service if used; Kiu does not provision those dependencies.

### Ruby in Docker Compose

Run Kiu on the host. `app` is an example service name; replace it with the service
and command your project uses. `-T` disables pseudo-TTY allocation for automation.
Keep `-T` in automated invocations: Kiu does not provide terminal job control.

| Task | Example command |
| --- | --- |
| Project CI script | `kiu docker compose exec -T app bin/ci` |
| Rails / Minitest suite | `kiu docker compose exec -T app bin/rails test` |
| Rails / Minitest file | `kiu docker compose exec -T app bin/rails test test/models/user_test.rb` |
| Rails system tests | `kiu docker compose exec -T app bin/rails test:system` |
| RSpec suite | `kiu docker compose exec -T app bundle exec rspec` |
| RSpec example | `kiu docker compose exec -T app bundle exec rspec spec/models/user_spec.rb:42` |
| CI in a one-off container | `kiu docker compose run --rm -T app bin/ci` |

Do not add another Kiu inside the container. Docker does not automatically forward
Kiu's nesting marker: an inner Kiu may use an independent container pool rather
than being rejected. Never share a state directory across PID namespaces.
The slot covers the foreground Docker
client's lifetime; cancellation does not prove that work inside the container has
stopped. These are usage templates, **not completed Docker acceptance tests**.
`compose run` can start dependency services that outlive the command; those
services are not reserved by Kiu after the foreground client exits. See the
[Compose run documentation](https://docs.docker.com/reference/cli/docker/compose/run/).

### Dart: Flutter

| Task | Example command |
| --- | --- |
| Unit and widget tests | `kiu flutter test` |
| One test file | `kiu flutter test test/widget_test.dart` |
| Test coverage | `kiu flutter test --coverage` |
| Integration tests on a selected device | `kiu flutter test integration_test -d <device-id>` |
| One integration test | `kiu flutter test integration_test/app_test.dart -d <device-id>` |
| Android APK | `kiu flutter build apk` |
| Android App Bundle | `kiu flutter build appbundle` |
| iOS build (macOS host) | `kiu flutter build ios --no-codesign` |
| Web build | `kiu flutter build web` |
| macOS build (macOS host) | `kiu flutter build macos` |
| Linux build (Linux host) | `kiu flutter build linux` |

Replace `<device-id>` with an actual target; the angle brackets are placeholders,
not literal shell arguments. Keep project-specific flavors, entry points,
`--dart-define` values and other flags. The iOS example builds without signing;
use the project's existing signing setup when a signed build is required.
Some projects use `flutter drive` or a custom integration-test script instead.
Wrap the existing finite, noninteractive test command rather than changing its
runner. The test runner must finish and clean up the app it launches; do not
substitute a persistent application session for the test command.
Start emulators/devices separately, outside Kiu. These examples require
appropriate SDKs and targets and have **not been acceptance-tested through Kiu**.
Select a device explicitly for device-based integration tests to avoid a device
selection prompt. See [Flutter's integration-test guide](https://docs.flutter.dev/testing/integration-tests)
for target-specific setup; web tests may need a different runner.

### Go

| Task | Example command |
| --- | --- |
| Test suite | `kiu go test ./...` |
| Race detector | `kiu go test -race ./...` |
| One package | `kiu go test ./internal/example` |
| Named test | `kiu go test ./internal/example -run '^TestExample$'` |
| Build all packages | `kiu go build ./...` |

### Other finite, resource-intensive tasks

| Task | Example command |
| --- | --- |
| Docker image build | `kiu docker build .` |
| Compose image builds | `kiu docker compose build` |
| Mutation testing or large import | `kiu ./scripts/heavy-task.sh` |
| Existing project build/test script | `kiu ./scripts/ci.sh` |

Script paths and test filenames above are illustrative. Use only scripts and
commands that exist in your project. Do not wrap `docker compose up -d` expecting
to reserve capacity for the services it starts: the command returns while they
continue running. See [execution boundaries](docs/behavior.md#boundaries).

## Development

For Kiu's own test suite, run these commands directly, outside a Kiu-wrapped
command. The current test harness launches Kiu subprocesses and requires a clean
execution context; it does not yet clear an inherited nesting marker in every fixture.

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

Different `HOME` values (and, on Linux, `XDG_STATE_HOME`) can select a different pool, even for
the same user. Ensure agent sandboxes use the intended host state location; do
not use `sudo` or a container-local Kiu expecting to join your host user's pool.

The directory and lock file are created with modes 0700 and 0600 respectively.
An active reservation stores only a schema version, boot ID and process group ID.
Command arguments and environment are not persisted.
Admission order is best-effort, without a FIFO guarantee.
