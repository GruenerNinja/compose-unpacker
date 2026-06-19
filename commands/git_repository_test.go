package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portainer/compose-unpacker/exec"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestPrepareGitRepositoryKeepPreservesLocalChanges(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "repo")
	mountPath := filepath.Join(tmpDir, "dest", "stacks", "test-stack")
	clonePath := filepath.Join(mountPath, "repo")

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
	require.NoFileExists(t, filepath.Join(clonePath, "removed.txt"))
}

func TestPrepareGitRepositoryFlatModeClonesAndSyncsDestination(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "repo")
	destination := filepath.Join(tmpDir, "target")

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
	require.NoDirExists(t, filepath.Join(destination, "stacks"))
}

func TestPrepareGitRepositoryFlatKeepPreservesLocalChanges(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "repo")
	destination := filepath.Join(tmpDir, "target")

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
	require.NoFileExists(t, filepath.Join(destination, "removed.txt"))
	require.NoDirExists(t, filepath.Join(destination, "stacks"))
}

func TestValidateFlatDestination(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	allowedDestination := filepath.Join(tmpDir, "parent", "stack")

	require.NoError(t, validateFlatDestination(allowedDestination))
	require.DirExists(t, allowedDestination)
	require.Empty(t, listTestFiles(t, allowedDestination))

	for _, destination := range []string{
		"",
		string(filepath.Separator),
		filepath.Join(string(filepath.Separator), "etc"),
		filepath.Join(string(filepath.Separator), "etc", "portainer"),
		filepath.Join(string(filepath.Separator), "usr"),
		filepath.Join(string(filepath.Separator), "bin"),
		filepath.Join(string(filepath.Separator), "var", "run"),
		filepath.Join(string(filepath.Separator), "root"),
	} {
		err := validateFlatDestination(destination)
		require.Error(t, err)
		require.Contains(t, err.Error(), "flat destination")
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

	path := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func readTestFile(t *testing.T, root string, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
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
