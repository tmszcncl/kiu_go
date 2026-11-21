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
Nested invocations are rejected to avoid deadlock. Optional job names and
`kiu status` / `kiu status --json` describe running and waiting jobs.

A short-lived supervisor forwards cancellation to the job's Unix process group.
It allows five seconds for cleanup, then sends SIGKILL. A second cancellation
skips the grace period. Killing the client triggers the same cleanup. A durable
reservation prevents new admission if the supervisor is killed while work remains.

This is a development version for **noninteractive build and test commands**.
Interactive terminal programs and descendants that leave the managed process
group are outside its contract. Local Flutter and Docker/Rails tests have verified
shared-slot execution on macOS arm64. **Cancelling the Docker client can release
the slot while container work continues**; stop and verify that specific container
job before submitting replacement work. See [manual validation](docs/validation.md),
[behavior](docs/behavior.md) for failure policies and
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
`status`. Configuration commands are not implemented yet.

### Job names and queue status

Both forms accept a Kiu label before the child command:

```sh
kiu --name "Calculyo — system tests" docker compose exec -T app bin/rails test:system
kiu --name "Calculyo — system tests" run -- docker compose exec -T app bin/rails test:system
```

Without a label, the display name is the working directory's basename plus the
executable's basename, for example `calculyo_rails — docker`. Labels must contain
printable text, at most 512 UTF-8 bytes. Arguments after the child executable are
never parsed as Kiu flags: `kiu rails test --name example` passes `--name example`
to Rails. Do not include secrets in labels: names and working directories appear
in private local metadata and status output.

Outside opt-in Herdr reporting, a free slot starts the command without a Kiu banner. An invocation that has to
wait prints one snapshot to stderr:

```text
kiu: waiting for a slot — Calculyo — system tests
kiu: slot occupied by Tidudanka — full CI (running for 4m12s)
kiu: waiting jobs: 3 (including this job)
```

When admitted, it prints:

```text
kiu: starting — Calculyo — system tests (waited 1m8s)
```

The initial wait message is a snapshot. Run `kiu` with no arguments in a terminal
to open a full-screen queue view that refreshes once a second. Recent results
appear below the queue, newest first. The number visible depends on the terminal
height and the space used by the queue. Use the arrow keys or `j` / `k` to
scroll through the last 100 results. Press `q` or `Ctrl+C` to close the view.
Jobs keep running when you close it.
The live table shows CPU for the running job by summing the OS-reported usage
of its process group. One fully used logical CPU is 100%. Waiting jobs have a
blank CPU cell. macOS reports recent usage and Linux reports a lifetime average
per process.
For `docker compose exec [-T] SERVICE COMMAND`, Kiu looks up the service by
the job's working directory in the Docker context available to the viewer. If
exactly one running container matches, the cell shows that container's total CPU.
Unsupported Docker invocations and ambiguous matches leave the cell blank.

For a single snapshot, use either command below. With redirected input or output,
bare `kiu` prints a text snapshot followed by recent results. For usage help,
run `kiu help`.

```sh
kiu status
kiu status --json
```

The human-readable view uses one `ID STATUS NAME` table, with running
jobs before waiting jobs and no empty table when idle. The status includes elapsed time: running
time for active jobs and waiting time for queued jobs. Directories, submission/start
times and process IDs remain available in JSON; its schema is documented in [behavior](docs/behavior.md#status-json-schema-version-1).
The waiting count includes the invocation printing that wait message. Submission-time display
order does not promise FIFO, a queue position or an estimated start time.
An older binary or a brief admission transition may occupy the slot without
usable job details; Kiu reports that rather than inventing a name or start time.

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
- Give logical jobs a descriptive label, for example
  `kiu --name "project — system tests" <existing-command>`. The label goes before
  the command or `run --`, never after child arguments.
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
- Use `kiu status --json` for current running/waiting jobs. Treat it as a
  snapshot; do not invent queue positions, ETAs or missing job details.
  Kiu currently has one shared slot. Configuration, `--no-wait` and
  `--wait-timeout` are not implemented; do not invent these options.
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
client's lifetime. A controlled cancellation test confirmed that container work
can continue after the local client exits and Kiu releases the slot. Verify and
stop that specific container job before retrying; do not stop unrelated services.
A targeted Rails test and a local Flutter test have verified shared-slot behavior
in both directions on macOS arm64. Other examples, including full `bin/ci`, remain
usage templates. See [manual validation](docs/validation.md).
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
appropriate SDKs and targets. A targeted local `flutter test` has been tested
through Kiu alongside Docker/Rails on macOS arm64; the builds and device-based
integration examples above have not. See [manual validation](docs/validation.md).
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

### Python and Django

Use the project's virtual environment and dependencies; replace `python` with its
actual interpreter if needed. Django also needs its test settings and database
permissions configured.

| Task | Example command | Official reference |
| --- | --- | --- |
| pytest suite | `kiu python -m pytest` | [pytest CLI](https://docs.pytest.org/en/stable/how-to/usage.html) |
| One pytest file | `kiu python -m pytest tests/test_example.py` | [pytest selection](https://docs.pytest.org/en/stable/how-to/usage.html) |
| Standard-library unittest discovery | `kiu python -m unittest discover` | [unittest discovery](https://docs.python.org/3/library/unittest.html#test-discovery) |
| Django suite, without input prompts | `kiu python manage.py test --noinput` | [Django testing](https://docs.djangoproject.com/en/stable/topics/testing/overview/) |

### Node.js and TypeScript

Install the project's dependencies first. Use existing scripts from `package.json`;
`test:ci` and `build` below are example script names, not built-in npm tasks.
They must finish without watch mode. Preserve the project's TypeScript compilation
or loader setup rather than assuming every runner accepts TypeScript directly.

| Task | Example command | Official reference |
| --- | --- | --- |
| Node's built-in test runner | `kiu node --test` | [Node CLI](https://nodejs.org/api/cli.html#--test) |
| Existing finite test script | `kiu npm run test:ci` | [npm scripts](https://docs.npmjs.com/cli/v11/commands/npm-run/) |
| Existing production build script | `kiu npm run build` | [npm scripts](https://docs.npmjs.com/cli/v11/commands/npm-run/) |
| Installed Vitest, single run | `kiu ./node_modules/.bin/vitest run` | [Vitest CLI](https://vitest.dev/guide/cli.html) |
| Installed Playwright, no automatic HTML report server | `PLAYWRIGHT_HTML_OPEN=never kiu ./node_modules/.bin/playwright test` | [Playwright CLI](https://playwright.dev/docs/test-cli), [HTML reporter](https://playwright.dev/docs/test-reporters#html-reporter) |

The local executable paths require a `node_modules` installation containing those
tools; with another dependency layout, use the project's existing package-manager
script. Playwright needs its browsers and application/test configuration installed.
The environment assignment above is shell syntax placed **before Kiu**.
Do not add a universal `--watch=false`: runner options differ. Leave dev servers,
watchers and interactive test/report UIs outside the queue.

### .NET (C# and F#)

Use the SDK selected by the project (including `global.json`, when present),
restored dependencies and a project compatible with the macOS/Linux host. These
examples do not establish Windows, .NET Framework, WPF or WinForms support.
Replace the illustrative solution/project paths with actual files.

| Task | Example command | Official reference |
| --- | --- | --- |
| Test project | `kiu dotnet test tests/App.Tests/App.Tests.csproj` | [dotnet test](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-test) |
| Release build | `kiu dotnet build App.sln -c Release` | [dotnet build](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-build) |
| Publish local deployment artifacts | `kiu dotnet publish src/App/App.csproj -c Release` | [dotnet publish](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-publish) |

Use the project's test runner options; VSTest and Microsoft.Testing.Platform do
not accept identical flags. Publishing artifacts here does not deploy a service.

### Game engines and rendering

These are **project-specific templates**, with CLI syntax checked against the
linked official documentation. They have **not been acceptance-tested through
Kiu**. Install the matching engine, SDKs, export targets and any required licenses
before queueing work. Run finite automation, not the editor GUI or a game session.

#### Unity

Set `UNITY_EDITOR` to the actual editor executable for the project's Unity version,
not Unity Hub. Run from the project root, with the Unity Test Framework installed
and an existing `artifacts/` directory.

| Task | Example command |
| --- | --- |
| EditMode tests | `kiu "$UNITY_EDITOR" -batchmode -projectPath "$PWD" -runTests -testPlatform EditMode -testResults "$PWD/artifacts/editmode.xml" -logFile -` |
| Existing synchronous build method | `kiu "$UNITY_EDITOR" -batchmode -quit -projectPath "$PWD" -executeMethod BuildAutomation.Build -logFile -` |

Do **not** add `-quit` to the test invocation: it can terminate tests before they
finish. `BuildAutomation.Build` is an illustrative project-defined static method
in an Editor script, not a Unity built-in. It must report build failures through
an exception or a nonzero editor exit code; asynchronous build methods need their
own completion/exit handling. Graphics tests may still need a display/graphics
setup despite batch mode. See [Unity Test Framework CLI](https://docs.unity3d.com/Packages/com.unity.test-framework@1.4/manual/reference-command-line.html)
and [Editor CLI](https://docs.unity.com/en-us/engine/6000.0/manual/unity-editor/command-line-arguments/editor).

#### Unreal Engine

| Task | Example command |
| --- | --- |
| Existing project packaging/CI script | `kiu ./scripts/package-unreal.sh` |

The script path is illustrative: use an existing, finite project recipe that
calls `Engine/Build/BatchFiles/RunUAT.sh BuildCookRun` on macOS/Linux, waits for
completion and propagates failure. Project path, platform, target and cooking,
staging and packaging options depend on that recipe; there is no universal
packaging command here. See [Automation Tool](https://dev.epicgames.com/documentation/en-us/unreal-engine/unreal-automation-tool-overview-for-unreal-engine)
and [build operations](https://dev.epicgames.com/documentation/unreal-engine/build-operations-cooking-packaging-deploying-and-running-projects-in-unreal-engine).

#### Godot 4

| Task | Example command |
| --- | --- |
| Headless release export | `kiu godot --headless --path . --export-release "Linux" build/game.x86_64` |

Here `godot` must resolve to the matching editor executable. `Linux` is an
illustrative **existing preset name**, matched exactly in `export_presets.cfg`;
replace it and the output filename for your target. Install its export templates
and create `build/` first. Keep project-specific export validation in the existing
CI script. Project tests use the project's own runner, not a generic Godot test
flag. See [Godot command-line export](https://docs.godotengine.org/en/stable/tutorials/editor/command_line_tutorial.html#exporting).

#### Blender

| Task | Example command |
| --- | --- |
| Render frame 1 using the scene's render/output settings | `kiu blender --background scene.blend --render-frame 1` |
| Process a scene with an existing Python script | `kiu blender --background scene.blend --python-exit-code 1 --python scripts/process_scene.py` |

Use an installed Blender executable and existing scene/script paths. Argument
order matters: load the scene before rendering or running the script; any render
setting overrides go before `--render-frame`. `--python-exit-code 1` reports an
uncaught command-line Python exception as failure. See [Blender CLI arguments](https://docs.blender.org/manual/en/5.0/advanced/command_line/arguments.html).

### Other toolchains

Use installed project toolchains and existing executable wrappers/tasks. Examples
below assume macOS/Linux shell syntax and configured projects.

| Toolchain / task | Example command | Official reference |
| --- | --- | --- |
| Java/Kotlin: Gradle tests or build | `kiu ./gradlew test` / `kiu ./gradlew build` | [Gradle CLI](https://docs.gradle.org/current/userguide/command_line_interface.html) |
| Android: debug APK | `kiu ./gradlew assembleDebug` | [Android CLI builds](https://developer.android.com/build/building-cmdline) |
| Java: Maven verification lifecycle | `kiu ./mvnw verify` | [Maven wrapper](https://maven.apache.org/tools/wrapper/), [lifecycle](https://maven.apache.org/guides/introduction/introduction-to-the-lifecycle.html) |
| Rust: tests | `kiu cargo test` | [cargo test](https://doc.rust-lang.org/cargo/commands/cargo-test.html) |
| Rust: release build | `kiu cargo build --release` | [cargo build](https://doc.rust-lang.org/cargo/commands/cargo-build.html) |
| Rust: configured benchmarks | `kiu cargo bench` | [cargo bench](https://doc.rust-lang.org/cargo/commands/cargo-bench.html) |
| C/C++: configured CMake build tree | `kiu cmake --build build` | [CMake build](https://cmake.org/cmake/help/latest/manual/cmake.1.html#build-a-project) |
| C/C++: registered CTest tests | `kiu ctest --test-dir build --output-on-failure` | [CTest CLI](https://cmake.org/cmake/help/latest/manual/ctest.1.html) |

Gradle needs a compatible JDK and plugins defining the selected tasks; Android
also needs its SDK, Android Gradle plugin and matching variant task. Maven needs
a compatible JDK and project lifecycle configuration. Rust benchmarks need the
project's configured harness/toolchain (the built-in `#[bench]` harness needs
nightly). CMake needs an already configured build directory and registered tests;
`ctest --test-dir` requires CMake 3.20 or later. With multi-configuration generators,
select the project's configuration explicitly (`cmake --build build --config Release`
and `ctest --test-dir build -C Release --output-on-failure`).

A slot covers the foreground job and its managed process group, not a CPU/GPU
allocation. Persistent build servers, detached workers and services are subject
to the same [execution boundaries](docs/behavior.md#boundaries) as other commands.

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

## Optional Herdr App / Core status

Kiu can report its lifecycle to a **dedicated Herdr Core terminal** using OSC
7501. Opt in per command with `KIU_HERDR_STATUS=1`; normal invocations are silent
apart from normal diagnostics, and child stdout is unchanged. Reporting requires
`HC_TERMINAL_ID` and terminal stderr. No Herdr TUI integration is involved.

For agents using `hc`, first verify the parent conversation with `hc get` as
required by your project instructions, then launch:

```sh
hc run --bg --parent <verified-parent-id> --cwd <absolute-project-path> \
  --name project-tests-kiu env KIU_HERDR_STATUS=1 kiu --name "project — tests" go test ./...
```

Use **`<task>-kiu`** for the Herdr terminal name, matching `<task>-agy` and
`<task>-claude` for reviewers. For example, one task may have these child entries:

```text
unified-navigation-agy
unified-navigation-claude
unified-navigation-kiu
```

The `hc --name` identifies the terminal in Herdr; `kiu --name` is the readable
job label in the shared queue, for example `Calculyo — unified navigation tests`.
Keep the Herdr name within its 32-character limit. If separate Kiu jobs for a task
need distinct names, put the discriminator **before** the suffix, such as
`navigation-unit-kiu` and `navigation-system-kiu`. Keep `-kiu` at the end.
Names are descriptive conventions: always verify that the actual command uses
Kiu; a suffix alone does not put a command in the queue.

Replace both placeholders and use the project's real test/build command. Keep
the returned terminal ID, inspect it with `hc get` / `hc read`, and wait with
`hc wait`. Do not retry just because it is queued. Clean up that exact terminal
after collecting its exit status. To cancel, use `hc keys <id> ctrl+c`; the
generic `hc interrupt` may choose Escape for an unfamiliar program.
Do not set this option globally or enable it
inside an agent's own terminal: root reports belong to the dedicated job terminal.

Waiting for capacity reports **`working` with “Waiting for slot”**, never
`blocked`. Running, descendant cleanup, completion and failure also report status.
The chosen job name is included; command arguments are not copied into status.
The stock Herdr child list shows the `hc` task name and a state indicator; the
detailed message and terminal title are available when inspecting the task.
Kiu also prints a readable line for each phase to stderr, so a quiet command's
terminal still shows whether it is starting, waiting, running or finished.
Showing that message directly in the child row requires a Herdr UI customization.
While waiting, the initial Herdr message also names the occupant when its
metadata is available. Use `kiu status --json` for a fresh snapshot.

After updating the source, reinstall with `go install ./cmd/kiu` before launching
new tasks. Already-running jobs keep their original binary and reporting mode;
do not restart CI solely to enable status reporting. Ask active agents to reread
their updated project instructions and use the dedicated-terminal command above
for subsequent jobs.

Keep the protected command's stdout/stderr attached to its Herdr terminal so the
peek shows live test/build output. For example, run `kiu docker compose exec -T
app bin/ci` rather than redirecting all of `bin/ci` into a container log file.
If a file log is required, use an existing project-approved streaming logger that
preserves the command's exit code; a plain `command | tee log` can mask failure.
Kiu does not read or replay files that a child redirects its output into.

## Development

Kiu's test harness starts real subprocesses in isolated temporary pools and resets
inherited execution markers in those fixtures. It does not use your live job pool.

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
An active reservation contains a schema version, boot ID, process group ID and
job metadata. Per-job records and ownership locks describe waiting supervisors.
Metadata includes the name, executable basename, directory, process IDs and
timestamps. Command arguments and environment are not persisted. Completed
records are removed; abandoned ownership records are pruned under coordination.
Admission order is best-effort, without a FIFO guarantee.
