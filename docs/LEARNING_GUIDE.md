# Learning Compose Unpacker: Go from Zero

This guide assumes you can read Java but have not used Go. Read it once from top to
bottom, then keep it open while following the code in your editor.

## 1. The smallest useful Go vocabulary

### Packages and imports

Every `.go` file begins with a package name:

```go
package commands
```

A Go package is close to a Java package, but all `.go` files in the same directory
normally belong to the same package and compile together. There are no classes that
own every function. A function can belong directly to a package.

Imports name the packages used by that file:

```go
import (
    "fmt"                       // Go standard library
    "github.com/rs/zerolog/log" // external module
)
```

Go removes unused imports at compile time. `gofmt` is the standard formatter and
also sorts imports into consistent groups.

### Structs instead of data classes

Go has structs, which are similar to Java classes that contain fields but do not
need inheritance or constructors:

```go
type RemoveDirCommand struct {
    Path string
}
```

Rough Java equivalent:

```java
final class RemoveDirCommand {
    String path;
}
```

A method is declared outside the struct. The receiver before the method name says
which type owns it:

```go
func (cmd *RemoveDirCommand) Run(...) error
```

`cmd *RemoveDirCommand` is roughly Java's `this`. The `*` means `cmd` points to the
original value, similar to using an object reference rather than a copy.

### Struct tags are metadata

The text in backticks after command fields is read by the Kong CLI library:

```go
Path string `arg:"" help:"The path to remove." name:"path"`
```

This resembles a Java annotation such as `@Option` or reflection metadata. Kong
uses it to decide whether a field is a flag or positional argument and to generate
help text.

### Functions can return more than one value

Go commonly returns a useful result and an error:

```go
deploymentDir, err := cleanDeploymentDir(cmd.DeploymentDir)
if err != nil {
    return err
}
```

`:=` declares and initializes local variables. Java usually represents this with
an exception or a result wrapper. In Go, callers explicitly check `err`; `nil`
means there was no error.

The compact pattern below means “call the function; if its error is not nil, handle
it here”:

```go
if err := os.RemoveAll(path); err != nil {
    return err
}
```

### `defer` is reliable cleanup

```go
file, err := os.Open(path)
if err != nil {
    return err
}
defer file.Close()
```

`defer` schedules the call for when the surrounding function returns. It plays a
role similar to Java's `finally` or try-with-resources. Deferred calls run in
last-in-first-out order.

### Slices and maps

`[]string` is a slice: a flexible view over an array, used much like a Java
`ArrayList<String>`. `append(slice, value)` adds a value and returns the possibly
resized slice.

`map[string]struct{}` is used as a set. The string is the key and the empty struct
uses no storage for a meaningful value. Java's closest equivalent is
`HashSet<String>`.

```go
if _, exists := seen[file]; exists {
    continue
}
seen[file] = struct{}{}
```

`_` is the blank identifier: it deliberately ignores a value.

### Interfaces are satisfied automatically

Go interfaces are implemented by having the required methods. A type does not say
`implements SomeInterface`. For example, anything with `Read([]byte) (int, error)`
satisfies `io.Reader`. That is why `readersEqual` can compare both normal files and
Git blob readers.

### Visibility follows capitalization

Names beginning with an uppercase letter are exported to other packages:
`DeployCommand`, `Run`, and `GetAuth`. Lowercase names such as
`cleanDeploymentDir` are package-private, similar to Java package visibility.

## 2. Start at `main.go`

All executable Go programs use `package main` and begin in `func main()`.

The anonymous `cli` struct lists global flags and subcommands. Kong fills its
fields from command-line input. For example, `deploy` selects `DeployCommand`, and
`--log-level DEBUG` sets `cli.LogLevel`.

The startup sequence is:

1. Create a root `context.Context`. Contexts carry cancellation and deadlines down
   a call tree. Java has no single standard equivalent; think of a cancellation
   token passed explicitly through services.
2. Parse arguments with `kong.Parse`.
3. Initialize FIPS behavior and logging.
4. Wrap the context in `CommandExecutionContext`.
5. Ask Kong to call the selected command's `Run` method.
6. Print an error and exit with code 255 if the command failed.

Follow these calls in this order:

```text
main.main
  -> kong.Parse
  -> exec.NewCommandExecutionContext
  -> DeployCommand.Run (or another selected command)
```

## 3. Follow a Compose deployment

Open `commands/compose_deploy.go`. `DeployCommand` holds every CLI option and
argument. Its `Run` method performs these steps:

1. Log the request without logging the password.
2. Derive a repository name from the URL.
3. Validate `--flat`, `--source-dir`, `--deployment-dir`, and Compose paths.
4. Calculate where the repository will live:
   - normal: `<destination>/stacks/<project>/<repository>`
   - flat: `<destination>`
5. Restore previously archived deployment files when that feature is active.
6. Call `prepareGitRepository` to clone or synchronize Git content.
7. Convert relative Compose paths to full filesystem paths.
8. Convert registry strings into Docker `AuthConfig` values.
9. Call Portainer's Compose deployer.
10. After success, optionally archive or delete deployment files.

Errors are wrapped with `%w`:

```go
return fmt.Errorf("%w: %w", exec.ErrDeployComposeFailure, err)
```

Wrapping keeps the original errors in an error chain, so callers can inspect them
with `errors.Is`. This is more like Java exception chaining than building a plain
error message.

`commands/swarm_deploy.go` has almost the same preparation flow, but it creates a
Swarm deployer and supplies Swarm-specific options such as `PullImage`.

## 4. Understand Git preparation

Most of the careful logic is in `commands/git_repository.go`.

### Normal clone

Without `--keep`, `cloneGitRepository` removes the previous mount directory and
performs a shallow clone (`Depth: 1`). It uses Portainer's no-symlink filesystem as
a security boundary for checked-out repository content.

### Keeping local changes

With `--keep`, the program does not use `git pull`. Instead it:

1. Finds files that are locally modified, missing, or untracked.
2. Marks them as protected.
3. Clones the requested revision into a temporary directory.
4. Copies clean repository files into the destination.
5. Skips protected local files.
6. Removes files deleted upstream only when they are not protected.
7. Replaces `.git` with metadata from the fresh clone.

This behavior is implemented by `protectedRepositoryPaths`, `trackedFileDirty`,
`syncRepositoryWorktree`, and `copyRepositoryFiles`.

### Selecting a source directory

`--source-dir services/proxy` treats only that repository directory as deployment
content. Its contents are copied to the destination root, not to
`destination/services/proxy`. Path-cleaning functions reject absolute paths,
Windows separators, and `..` traversal.

### Flat-mode safety

Flat mode may replace files directly in the chosen directory, so
`validateFlatDestination` requires an absolute, writable path and blocks dangerous
system locations such as `/`, `/etc`, `/usr`, and `/var/run`. It validates both the
written path and the path after resolving symbolic links.

## 5. Deployment-file lifecycle

`commands/deployment_files.go` manages only:

- every requested Compose file;
- `.env`;
- `portainer.yml`;
- `portainer.yaml`.

It deliberately leaves unrelated files alone. After a successful deployment it
can either:

- archive these files below `--deployment-dir`; or
- remove them with `--cleanup-deployment-files`.

Before the next deployment, archived files can be restored when their destination
is missing. Existing local files win. An archive target with different content is
also never overwritten. These rules prevent accidental loss of local changes.

## 6. Undeploy and utility commands

- `UndeployCommand.Run` calls the Compose deployer's `Remove`, optionally removes
  volumes, then removes the working directory unless `--keep` is set.
- `SwarmUndeployCommand.Run` does the corresponding Swarm operation.
- `RemoveDirCommand.Run` is a small wrapper around `os.RemoveAll`. Remember that
  `RemoveAll` is recursive, similar to recursively walking and deleting a Java
  `Path` tree.

## 7. Supporting packages

### `auth`

`GetAuth` returns `nil` for public repositories. If a password/token exists but no
username is supplied, it uses the conventional username `token`. `&http.BasicAuth`
returns a pointer to the newly created struct.

### `exec`

This project's `exec` package is not the standard library's `os/exec` package. It
contains:

- `CommandExecutionContext`, which carries `context.Context` into commands;
- the shared `ErrDeployComposeFailure` sentinel error;
- working-directory construction;
- registry credential parsing.

A registry value must be `username:password:server` or
`username:password:host:port`. The current parser splits on every colon, so values
containing colons do not fit this format.

### `log`

The logging package configures zerolog globally. Logging calls build an event and
finish it with `Msg`:

```go
log.Info().Str("repository", url).Msg("Cloning repository")
```

This is similar to a Java fluent logger. `--pretty-log` selects human-readable
console output; otherwise logs use zerolog's structured format.

## 8. How to make a change safely

Use this loop for your first contributions:

1. Find the CLI struct field that exposes the behavior.
2. Follow its use into the command's `Run` method.
3. Move complicated logic into a small package-private helper.
4. Add a table-driven or focused test in the same package.
5. Run `gofmt` on changed `.go` files.
6. Run the narrow test, then all tests, then the linter.

```bash
gofmt -w path/to/changed.go
go test ./commands -run TestName -v
go test ./...
make lint
```

Go tests use the standard `testing` package. Files named `*_test.go` compile only
for tests. This repository also uses `testify/require`; a failed `require` stops
the current test immediately, like a Java assertion that aborts the test method.

`t.TempDir()` creates an isolated temporary directory and cleans it automatically.
Many tests call `t.Parallel()`, which allows independent tests to run concurrently.
Do not make them share mutable global or filesystem state.

## 9. Good first exercises

Try these in order:

1. Add a test case for malformed registry credentials in `exec/utils.go`.
2. Improve one CLI help string and check the result with `go run . --help`.
3. Add a debug log that reports how many protected files a Git sync found.
4. Refactor the duplicated Compose and Swarm path preparation into a helper, while
   keeping all tests green.

The first two teach the edit-test loop. The last two introduce project behavior
without requiring you to understand Docker internals first.
