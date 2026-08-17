# Compose Unpacker

Compose Unpacker is a small command-line program used by Portainer. It downloads a
Git repository and asks Docker Compose or Docker Swarm to deploy the Compose files
inside it. It can also remove a deployed stack or delete a working directory.

If you are new to Go, start with the
[beginner's code walkthrough](docs/LEARNING_GUIDE.md). It explains the project from
the entry point onward and compares unfamiliar Go syntax with Java. For exact CLI
shapes, see the [command reference](docs/COMMAND_REFERENCE.md).

## What the program does

For a deployment, the program follows this path:

```text
command-line arguments
        |
        v
main.go parses a command
        |
        v
commands/* validates paths and prepares the Git repository
        |
        v
Portainer's libstack deployer calls Docker Compose or Docker Swarm
        |
        v
optional deployment files are archived or removed
```

The main commands are:

| Command | Purpose |
| --- | --- |
| `deploy` | Deploy a normal Docker Compose stack. |
| `undeploy` | Remove a normal Docker Compose stack. |
| `swarm-deploy` | Deploy a Docker Swarm stack. |
| `swarm-undeploy` | Remove a Docker Swarm stack. |
| `remove-dir` | Recursively remove a directory. |

Run `compose-unpacker --help` or `compose-unpacker <command> --help` to see the
arguments and options generated from the structs in `main.go` and `commands/`.

## Requirements

- Go matching the version declared in `go.mod`
- Docker when you want to perform a real deployment
- A local checkout of `github.com/portainer/portainer` at `../portainer`

The last requirement exists because `go.mod` contains:

```go
replace github.com/portainer/portainer => ../portainer
```

Go therefore uses the sibling `portainer` directory instead of downloading that
module. Remove the `replace` line only if you deliberately want to use the remote
version listed in `go.mod`.

## Build and test

```bash
# Compile the executable into dist/compose-unpacker.
make

# Run all Go tests.
make test

# Run the linter (requires golangci-lint).
make lint

# Build the executable and a Docker image.
make image
```

For a quicker development loop, the normal Go commands also work:

```bash
go test ./...
go test ./commands -run TestCleanDeploymentDir -v
go run . --help
```

## Example

The CLI arguments are positional, so their order matters. This example deploys a
Compose project called `mystack` from the repository's `master` branch:

```bash
./dist/compose-unpacker deploy \
  https://github.com/deviantony/docker-workbench.git \
  refs/heads/master \
  mystack \
  /tmp/unpacker \
  compose/relative-paths/web-static-content/docker-compose.yml
```

The same program can run in its container image:

```bash
docker run --rm \
  -v /tmp/unpacker:/tmp/unpacker \
  -v /var/run/docker.sock:/var/run/docker.sock \
  portainer/compose-unpacker deploy \
  https://github.com/deviantony/docker-workbench.git \
  refs/heads/master \
  mystack \
  /tmp/unpacker \
  compose/relative-paths/web-static-content/docker-compose.yml
```

The host and container destination paths must match. For example,
`-v /tmp/unpacker:/tmp/unpacker` works, while mounting a different host path at
`/tmp/unpacker` can break relative bind mounts from the Compose file.

## Useful deployment options

- `--keep` preserves locally changed or untracked repository files while syncing
  clean files from the new Git revision.
- `--flat` places repository files directly in `destination`. Without it, files go
  under `destination/stacks/<project>/<repository>`.
- `--source-dir` selects one repository subdirectory and copies its contents to the
  destination root. It is especially useful for monorepos.
- `--deployment-dir` archives the Compose file, `.env`, `portainer.yml`, and
  `portainer.yaml` after a successful deployment.
- `--cleanup-deployment-files` deletes those deployment files after success.
- `--skip-tls-verify` disables Git TLS verification only when the configured FIPS
  mode permits it. Avoid this option unless it is necessary.

## Project layout

```text
main.go                 CLI setup and program entry point
auth/                   Git username/password construction
commands/               deploy, undeploy, Git sync, and file-management logic
exec/                   shared execution context, errors, and argument parsing
log/                    zerolog configuration
build/                  Linux and Windows container images
docs/LEARNING_GUIDE.md  beginner-oriented tour of the code
docs/COMMAND_REFERENCE.md CLI arguments, flags, and path behavior
```

## Security

For information about reporting security vulnerabilities, see the
[Security Policy](SECURITY.md).
