package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portainer/compose-unpacker/exec"
	"github.com/portainer/portainer/api/filesystem"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestPrepareGitRepositoryKeepPreservesLocalChanges(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filesystem.JoinPaths(tmpDir, "repo")
	mountPath := filesystem.JoinPaths(tmpDir, "dest", "stacks", "test-stack")
	clonePath := filesystem.JoinPaths(mountPath, "repo")

	repo := initTestRepository(t, repoPath)
	writeTestFile(t, repoPath, "docker-compose.yml", "services:\n  app:\n    image: alpine:3.20\n")
	writeTestFile(t, repoPath, "config.txt", "repo-value\n")
	writeTestFile(t, repoPath, "clean.txt", "old-clean\n")
	writeTestFile(t, repoPath, "removed.txt", "remove-me\n")
	commitTestRepository(t, repo, "initial")

	ctx := exec.NewCommandExecutionContext(context.Background())
	err := prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		mountPath:  mountPath,
		clonePath:  clonePath,
	})
	require.NoError(t, err)

	writeTestFile(t, clonePath, "config.txt", "local-change\n")
	writeTestFile(t, clonePath, "future.txt", "local-only\n")

	writeTestFile(t, repoPath, "config.txt", "repo-new-value\n")
	writeTestFile(t, repoPath, "clean.txt", "repo-clean-update\n")
	writeTestFile(t, repoPath, "future.txt", "repo-future\n")
	writeTestFile(t, repoPath, "new.txt", "repo-new-file\n")
	removeTestRepositoryFile(t, repo, "removed.txt")
	commitTestRepository(t, repo, "update")

	err = prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		keep:       true,
		mountPath:  mountPath,
		clonePath:  clonePath,
	})
	require.NoError(t, err)

	require.Equal(t, "local-change\n", readTestFile(t, clonePath, "config.txt"))
	require.Equal(t, "repo-clean-update\n", readTestFile(t, clonePath, "clean.txt"))
	require.Equal(t, "local-only\n", readTestFile(t, clonePath, "future.txt"))
	require.Equal(t, "repo-new-file\n", readTestFile(t, clonePath, "new.txt"))
	require.NoFileExists(t, filesystem.JoinPaths(clonePath, "removed.txt"))
}

func TestPrepareGitRepositoryRedeployUpdatesComposeFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filesystem.JoinPaths(tmpDir, "repo")
	mountPath := filesystem.JoinPaths(tmpDir, "dest", "stacks", "test-stack")
	clonePath := filesystem.JoinPaths(mountPath, "repo")

	repo := initTestRepository(t, repoPath)
	writeTestFile(t, repoPath, "docker-compose.yml", "services:\n  app:\n    image: alpine:3.20\n")
	writeTestFile(t, repoPath, ".env", "VALUE=repository\n")
	commitTestRepository(t, repo, "initial")

	ctx := exec.NewCommandExecutionContext(context.Background())
	opts := gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		mountPath:  mountPath,
		clonePath:  clonePath,
	}
	require.NoError(t, prepareGitRepository(ctx, opts))

	// Simulate a user-managed environment file and a newer Compose definition in Git.
	writeTestFile(t, clonePath, ".env", "VALUE=local\n")
	writeTestFile(t, repoPath, "docker-compose.yml", "services:\n  app:\n    image: alpine:3.21\n")
	writeTestFile(t, repoPath, ".env", "VALUE=repository-updated\n")
	commitTestRepository(t, repo, "update compose")

	// Redeploy uses keep=true: clean repository files must still update, while local
	// modifications remain protected.
	opts.keep = true
	require.NoError(t, prepareGitRepository(ctx, opts))

	require.Equal(t, "services:\n  app:\n    image: alpine:3.21\n", readTestFile(t, clonePath, "docker-compose.yml"))
	require.Equal(t, "VALUE=local\n", readTestFile(t, clonePath, ".env"))
}

func TestPrepareGitRepositoryFlatModeClonesAndSyncsDestination(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filesystem.JoinPaths(tmpDir, "repo")
	destination := filesystem.JoinPaths(tmpDir, "target")

	repo := initTestRepository(t, repoPath)
	writeTestFile(t, repoPath, "docker-compose.yml", "services:\n  app:\n    image: alpine:3.20\n")
	writeTestFile(t, repoPath, ".env", "VALUE=repo\n")
	writeTestFile(t, repoPath, "traefik/dynamic/npm-fallback.yaml", "http:\n  routers: {}\n")
	commitTestRepository(t, repo, "initial")

	mountPath, clonePath := gitRepositoryDeploymentPaths(destination, "test-stack", "repo", true)
	require.Equal(t, destination, mountPath)
	require.Equal(t, destination, clonePath)

	require.NoError(t, validateFlatDestination(destination))

	ctx := exec.NewCommandExecutionContext(context.Background())
	err := prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		mountPath:  mountPath,
		clonePath:  clonePath,
	})
	require.NoError(t, err)

	require.Equal(t, "services:\n  app:\n    image: alpine:3.20\n", readTestFile(t, destination, "docker-compose.yml"))
	require.Equal(t, "VALUE=repo\n", readTestFile(t, destination, ".env"))
	require.Equal(t, "http:\n  routers: {}\n", readTestFile(t, destination, "traefik/dynamic/npm-fallback.yaml"))
	require.NoDirExists(t, filesystem.JoinPaths(destination, "stacks"))
}

func TestPrepareGitRepositoryFlatSourceDirClonesOnlySourceDirectory(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filesystem.JoinPaths(tmpDir, "repo")
	destination := filesystem.JoinPaths(tmpDir, "target")

	repo := initTestRepository(t, repoPath)
	writeTestFile(t, repoPath, "tmc-proxy/docker-compose.yml", "services:\n  app:\n    image: alpine:3.20\n")
	writeTestFile(t, repoPath, "tmc-proxy/.env", "VALUE=repo\n")
	writeTestFile(t, repoPath, "tmc-proxy/traefik/dynamic/test.yaml", "http:\n  routers: {}\n")
	writeTestFile(t, repoPath, "other/docker-compose.yml", "services:\n  other:\n    image: alpine\n")
	commitTestRepository(t, repo, "initial")

	mountPath, clonePath := gitRepositoryDeploymentPaths(destination, "test-stack", "repo", true)
	require.NoError(t, validateFlatDestination(destination))

	ctx := exec.NewCommandExecutionContext(context.Background())
	err := prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		keep:       true,
		mountPath:  mountPath,
		clonePath:  clonePath,
		sourceDir:  "tmc-proxy",
	})
	require.NoError(t, err)

	require.Equal(t, "services:\n  app:\n    image: alpine:3.20\n", readTestFile(t, destination, "docker-compose.yml"))
	require.Equal(t, "VALUE=repo\n", readTestFile(t, destination, ".env"))
	require.Equal(t, "http:\n  routers: {}\n", readTestFile(t, destination, "traefik/dynamic/test.yaml"))
	require.NoDirExists(t, filesystem.JoinPaths(destination, "tmc-proxy"))
	require.NoDirExists(t, filesystem.JoinPaths(destination, "other"))
	require.NoDirExists(t, filesystem.JoinPaths(destination, "stacks"))
}

func TestPrepareGitRepositoryFlatKeepPreservesLocalChanges(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filesystem.JoinPaths(tmpDir, "repo")
	destination := filesystem.JoinPaths(tmpDir, "target")

	repo := initTestRepository(t, repoPath)
	writeTestFile(t, repoPath, "docker-compose.yml", "services:\n  app:\n    image: alpine:3.20\n")
	writeTestFile(t, repoPath, ".env", "VALUE=repo\n")
	writeTestFile(t, repoPath, "traefik/dynamic/test.yaml", "http:\n  routers: {}\n")
	writeTestFile(t, repoPath, "clean.txt", "old-clean\n")
	writeTestFile(t, repoPath, "removed.txt", "remove-me\n")
	commitTestRepository(t, repo, "initial")

	mountPath, clonePath := gitRepositoryDeploymentPaths(destination, "test-stack", "repo", true)
	ctx := exec.NewCommandExecutionContext(context.Background())

	require.NoError(t, validateFlatDestination(destination))
	err := prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		keep:       true,
		mountPath:  mountPath,
		clonePath:  clonePath,
	})
	require.NoError(t, err)

	writeTestFile(t, destination, ".env", "VALUE=local\n")
	writeTestFile(t, destination, "traefik/dynamic/test.yaml", "http:\n  routers:\n    local: {}\n")
	writeTestFile(t, repoPath, "docker-compose.yml", "services:\n  app:\n    image: alpine:3.21\n")
	writeTestFile(t, repoPath, ".env", "VALUE=repo-updated\n")
	writeTestFile(t, repoPath, "traefik/dynamic/test.yaml", "http:\n  routers:\n    repo: {}\n")
	writeTestFile(t, repoPath, "clean.txt", "new-clean\n")
	writeTestFile(t, repoPath, "new.txt", "new-file\n")
	removeTestRepositoryFile(t, repo, "removed.txt")
	commitTestRepository(t, repo, "update")

	require.NoError(t, validateFlatDestination(destination))
	err = prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		keep:       true,
		mountPath:  mountPath,
		clonePath:  clonePath,
	})
	require.NoError(t, err)

	require.Equal(t, "services:\n  app:\n    image: alpine:3.21\n", readTestFile(t, destination, "docker-compose.yml"))
	require.Equal(t, "VALUE=local\n", readTestFile(t, destination, ".env"))
	require.Equal(t, "http:\n  routers:\n    local: {}\n", readTestFile(t, destination, "traefik/dynamic/test.yaml"))
	require.Equal(t, "new-clean\n", readTestFile(t, destination, "clean.txt"))
	require.Equal(t, "new-file\n", readTestFile(t, destination, "new.txt"))
	require.NoFileExists(t, filesystem.JoinPaths(destination, "removed.txt"))
	require.NoDirExists(t, filesystem.JoinPaths(destination, "stacks"))
}

func TestPrepareGitRepositoryFlatSourceDirKeepPreservesLocalChanges(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filesystem.JoinPaths(tmpDir, "repo")
	destination := filesystem.JoinPaths(tmpDir, "target")

	repo := initTestRepository(t, repoPath)
	writeTestFile(t, repoPath, "tmc-proxy/docker-compose.yml", "services:\n  app:\n    image: alpine:3.20\n")
	writeTestFile(t, repoPath, "tmc-proxy/.env", "VALUE=repo\n")
	writeTestFile(t, repoPath, "tmc-proxy/traefik/dynamic/test.yaml", "http:\n  routers: {}\n")
	writeTestFile(t, repoPath, "tmc-proxy/clean.txt", "old-clean\n")
	writeTestFile(t, repoPath, "tmc-proxy/removed.txt", "remove-me\n")
	writeTestFile(t, repoPath, "outside.txt", "do-not-copy\n")
	commitTestRepository(t, repo, "initial")

	mountPath, clonePath := gitRepositoryDeploymentPaths(destination, "test-stack", "repo", true)
	ctx := exec.NewCommandExecutionContext(context.Background())

	require.NoError(t, validateFlatDestination(destination))
	err := prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		keep:       true,
		mountPath:  mountPath,
		clonePath:  clonePath,
		sourceDir:  "tmc-proxy",
	})
	require.NoError(t, err)

	writeTestFile(t, destination, ".env", "VALUE=local\n")
	writeTestFile(t, destination, "docker-compose.yml", "services:\n  app:\n    image: local\n")
	writeTestFile(t, destination, "traefik/dynamic/test.yaml", "http:\n  routers:\n    local: {}\n")
	writeTestFile(t, repoPath, "tmc-proxy/docker-compose.yml", "services:\n  app:\n    image: alpine:3.21\n")
	writeTestFile(t, repoPath, "tmc-proxy/.env", "VALUE=repo-updated\n")
	writeTestFile(t, repoPath, "tmc-proxy/traefik/dynamic/test.yaml", "http:\n  routers:\n    repo: {}\n")
	writeTestFile(t, repoPath, "tmc-proxy/clean.txt", "new-clean\n")
	writeTestFile(t, repoPath, "tmc-proxy/new.txt", "new-file\n")
	writeTestFile(t, repoPath, "outside.txt", "still-do-not-copy\n")
	removeTestRepositoryFile(t, repo, "tmc-proxy/removed.txt")
	commitTestRepository(t, repo, "update")

	require.NoError(t, validateFlatDestination(destination))
	err = prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		keep:       true,
		mountPath:  mountPath,
		clonePath:  clonePath,
		sourceDir:  "tmc-proxy",
	})
	require.NoError(t, err)

	require.Equal(t, "services:\n  app:\n    image: local\n", readTestFile(t, destination, "docker-compose.yml"))
	require.Equal(t, "VALUE=local\n", readTestFile(t, destination, ".env"))
	require.Equal(t, "http:\n  routers:\n    local: {}\n", readTestFile(t, destination, "traefik/dynamic/test.yaml"))
	require.Equal(t, "new-clean\n", readTestFile(t, destination, "clean.txt"))
	require.Equal(t, "new-file\n", readTestFile(t, destination, "new.txt"))
	require.NoFileExists(t, filesystem.JoinPaths(destination, "removed.txt"))
	require.NoFileExists(t, filesystem.JoinPaths(destination, "outside.txt"))
	require.NoDirExists(t, filesystem.JoinPaths(destination, "tmc-proxy"))
	require.NoDirExists(t, filesystem.JoinPaths(destination, "stacks"))
}

func TestPrepareGitRepositoryFlatSourceDirManagedFilesRestoresMissingDeploymentFiles(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filesystem.JoinPaths(tmpDir, "repo")
	destination := filesystem.JoinPaths(tmpDir, "target")

	repo := initTestRepository(t, repoPath)
	writeTestFile(t, repoPath, "tmc-proxy/docker-compose.yml", "services:\n  app:\n    image: alpine:3.20\n")
	writeTestFile(t, repoPath, "tmc-proxy/.env", "VALUE=repo\n")
	writeTestFile(t, repoPath, "tmc-proxy/portainer.yml", "version: 1\n")
	writeTestFile(t, repoPath, "tmc-proxy/traefik/dynamic/test.yaml", "http:\n  routers: {}\n")
	commitTestRepository(t, repo, "initial")

	mountPath, clonePath := gitRepositoryDeploymentPaths(destination, "test-stack", "repo", true)
	ctx := exec.NewCommandExecutionContext(context.Background())

	require.NoError(t, validateFlatDestination(destination))
	err := prepareGitRepository(ctx, gitRepositoryOptions{
		repository: repoPath,
		reference:  plumbing.NewBranchReferenceName("master").String(),
		keep:       true,
		mountPath:  mountPath,
		clonePath:  clonePath,
		sourceDir:  "tmc-proxy",
	})
	require.NoError(t, err)

	managedFiles := deploymentFiles([]string{"docker-compose.yml"})
	require.NoError(t, finalizeDeploymentFiles(destination, "", managedFiles, true))
	require.NoFileExists(t, filesystem.JoinPaths(destination, "docker-compose.yml"))
	require.NoFileExists(t, filesystem.JoinPaths(destination, ".env"))
	require.NoFileExists(t, filesystem.JoinPaths(destination, "portainer.yml"))

	writeTestFile(t, repoPath, "tmc-proxy/docker-compose.yml", "services:\n  app:\n    image: alpine:3.21\n")
	writeTestFile(t, repoPath, "tmc-proxy/.env", "VALUE=repo-updated\n")
	writeTestFile(t, repoPath, "tmc-proxy/portainer.yml", "version: 1\n")
	commitTestRepository(t, repo, "update deployment files")

	err = prepareGitRepository(ctx, gitRepositoryOptions{
		repository:            repoPath,
		reference:             plumbing.NewBranchReferenceName("master").String(),
		keep:                  true,
		mountPath:             mountPath,
		clonePath:             clonePath,
		sourceDir:             "tmc-proxy",
		unprotectMissingPaths: deploymentFileSet(managedFiles),
	})
	require.NoError(t, err)

	require.Equal(t, "services:\n  app:\n    image: alpine:3.21\n", readTestFile(t, destination, "docker-compose.yml"))
	require.Equal(t, "VALUE=repo-updated\n", readTestFile(t, destination, ".env"))
	require.Equal(t, "version: 1\n", readTestFile(t, destination, "portainer.yml"))
	require.Equal(t, "http:\n  routers: {}\n", readTestFile(t, destination, "traefik/dynamic/test.yaml"))
}

func TestValidateFlatDestination(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	allowedDestination := filesystem.JoinPaths(tmpDir, "parent", "stack")

	require.NoError(t, validateFlatDestination(allowedDestination))
	require.DirExists(t, allowedDestination)
	require.Empty(t, listTestFiles(t, allowedDestination))

	for _, destination := range []string{
		"",
		string(filepath.Separator),
		filesystem.JoinPaths(string(filepath.Separator), "etc"),
		filesystem.JoinPaths(string(filepath.Separator), "etc", "portainer"),
		filesystem.JoinPaths(string(filepath.Separator), "usr"),
		filesystem.JoinPaths(string(filepath.Separator), "bin"),
		filesystem.JoinPaths(string(filepath.Separator), "var", "run"),
		filesystem.JoinPaths(string(filepath.Separator), "root"),
	} {
		err := validateFlatDestination(destination)
		require.Error(t, err)
		require.Contains(t, err.Error(), "flat destination")
	}
}

func TestStripRepositorySourceDir(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		filePath string
		source   string
		expected string
	}{
		{name: "strips matching source prefix", filePath: "tmc-proxy/docker-compose.yml", source: "tmc-proxy", expected: "docker-compose.yml"},
		{name: "strips leading slash and source prefix", filePath: "/tmc-proxy/docker-compose.yml", source: "tmc-proxy", expected: "docker-compose.yml"},
		{name: "keeps already stripped path", filePath: "docker-compose.yml", source: "tmc-proxy", expected: "docker-compose.yml"},
		{name: "keeps nested already stripped path", filePath: "compose/docker-compose.yml", source: "tmc-proxy", expected: "compose/docker-compose.yml"},
		{name: "cleans paths", filePath: "tmc-proxy/./compose.yml", source: "tmc-proxy", expected: "compose.yml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			actual, err := stripRepositorySourceDir(test.filePath, test.source)
			require.NoError(t, err)
			require.Equal(t, test.expected, actual)
		})
	}
}

func initTestRepository(t *testing.T, path string) *git.Repository {
	t.Helper()

	require.NoError(t, os.MkdirAll(path, 0755))
	repo, err := git.PlainInit(path, false)
	require.NoError(t, err)
	return repo
}

func commitTestRepository(t *testing.T, repo *git.Repository, message string) {
	t.Helper()

	worktree, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, worktree.AddGlob("."))
	_, err = worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Portainer Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)
}

func removeTestRepositoryFile(t *testing.T, repo *git.Repository, name string) {
	t.Helper()

	worktree, err := repo.Worktree()
	require.NoError(t, err)
	_, err = worktree.Remove(name)
	require.NoError(t, err)
}

func writeTestFile(t *testing.T, root string, name string, content string) {
	t.Helper()

	path := filesystem.JoinPaths(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func readTestFile(t *testing.T, root string, name string) string {
	t.Helper()

	content, err := os.ReadFile(filesystem.JoinPaths(root, name))
	require.NoError(t, err)
	return string(content)
}

func listTestFiles(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}
