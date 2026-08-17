# Command Reference

Kong generates the CLI from the structs in `main.go` and `commands/`. The safest
source for the installed binary's exact help is always:

```bash
compose-unpacker --help
compose-unpacker <command> --help
```

Arguments shown inside `<angle brackets>` are positional. Flags can be placed
before them. `COMPOSE_FILE...` means one or more paths may be provided.

## Global flags

These flags apply to every command:

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `--log-level` | `LOG_LEVEL` | `INFO` | One of `DEBUG`, `INFO`, `WARN`, or `ERROR`. |
| `--pretty-log` | `PRETTY_LOG` | `false` | Print human-readable colored logs. |
| `--fips-mode` | `FIPS_MODE` | `false` | Initialize Portainer's FIPS behavior. |

## `deploy`

Deploy a normal Docker Compose project:

```text
compose-unpacker deploy [flags] \
  <GIT_REPO> <GIT_REF> <PROJECT_NAME> <DESTINATION> <COMPOSE_FILE...>
```

Positional arguments:

| Argument | Meaning |
| --- | --- |
| `GIT_REPO` | Clone URL, such as `https://github.com/org/repo.git`. |
| `GIT_REF` | Full Git reference, such as `refs/heads/main`. |
| `PROJECT_NAME` | Docker Compose project/stack name. |
| `DESTINATION` | Base directory used for repository files. |
| `COMPOSE_FILE...` | One or more repository-relative Compose paths. |

Flags:

| Flag | Short | Meaning |
| --- | --- | --- |
| `--user` | `-u` | Git username. |
| `--password` | `-p` | Git password or personal access token. |
| `--prune` | `-r` | Remove services that are no longer in the Compose model. |
| `--keep` | `-k` | Preserve locally modified and untracked files while syncing. |
| `--flat` | | Use the destination itself instead of a nested stack path. |
| `--source-dir` | | Deploy one repository subdirectory at the destination root. |
| `--deployment-dir` | | Archive deployment files below this relative directory after success. |
| `--cleanup-deployment-files` | | Delete deployment files after success. |
| `--skip-tls-verify` | | Ask Git to skip TLS verification when FIPS policy allows it. |
| `--force-recreate` | | Recreate the stack even when image hashes did not change. |
| `--env KEY=VALUE` | | Add a stack environment value; repeat for multiple values. |
| `--registry VALUE` | | Add registry credentials; repeat for multiple registries. |

Registry values use `username:password:server` or
`username:password:host:port`. A value with the wrong number of colon-separated
parts is logged and skipped.

## `undeploy`

Remove a normal Docker Compose project:

```text
compose-unpacker undeploy [flags] \
  <GIT_REPO> <PROJECT_NAME> <DESTINATION> <COMPOSE_FILE...>
```

The current implementation removes the Docker project by `PROJECT_NAME`. It uses
`GIT_REPO` only for basic URL validation, and accepts Git credentials and Compose
paths for CLI compatibility without using them during removal.

| Flag | Short | Meaning |
| --- | --- | --- |
| `--user` | `-u` | Accepted for compatibility; currently unused during removal. |
| `--password` | `-p` | Accepted for compatibility; currently unused during removal. |
| `--keep` | `-k` | Keep local working files after removing the Docker project. |
| `--flat` | | Treat `DESTINATION` as the working directory. |
| `--remove-volumes` | `-v` | Remove the project's volumes too. |

## `swarm-deploy`

Deploy a Docker Swarm stack:

```text
compose-unpacker swarm-deploy [flags] \
  <GIT_REPO> <GIT_REF> <PROJECT_NAME> <DESTINATION> <COMPOSE_FILE...>
```

It accepts the same Git, path, deployment-file, environment, registry, prune, and
force-recreate flags as `deploy`. It also accepts `--pull` (`-f`) to pull images.
The deployment backend is Docker Swarm rather than normal Compose.

## `swarm-undeploy`

Remove a Docker Swarm stack:

```text
compose-unpacker swarm-undeploy [flags] <PROJECT_NAME> <DESTINATION>
```

`--keep` (`-k`) keeps the working files. `--flat` treats `DESTINATION` as the
working directory and applies flat-destination safety validation.

## `remove-dir`

Recursively remove a path:

```text
compose-unpacker remove-dir <PATH>
```

This command calls `os.RemoveAll`, so it removes all children and does not fail
merely because the path is already absent. Use it carefully.

## Destination behavior

Normal mode uses these paths:

```text
<destination>/stacks/<project-name>/<repository-name>
```

The middle stack directory is removed after undeploy unless `--keep` is set.

Flat mode clones or synchronizes directly into `<destination>`. The destination
must be absolute and writable. Important system paths and their descendants are
rejected, including `/`, `/bin`, `/dev`, `/etc`, `/proc`, `/run`, `/root`, `/sbin`,
`/sys`, `/usr`, and `/var/run` (using the matching platform separator).

When `--source-dir` is used, the selected directory's children appear directly in
the destination. For example, `--source-dir apps/proxy` turns
`apps/proxy/docker-compose.yml` into `docker-compose.yml` in the destination.
