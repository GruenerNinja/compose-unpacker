package commands

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanDeploymentDir(t *testing.T) {
	t.Parallel()

	cleaned, err := cleanDeploymentDir(" .deployed ")
	require.NoError(t, err)
	require.Equal(t, ".deployed", cleaned)

	cleaned, err = cleanDeploymentDir("deploy/files")
	require.NoError(t, err)
	require.Equal(t, "deploy/files", cleaned)

	for _, dir := range []string{".", "..", "../deploy", "/tmp/deploy", `deploy\files`} {
		_, err := cleanDeploymentDir(dir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "deployment directory")
	}
}

func TestDeploymentFilesIncludesOnlyDeploymentFiles(t *testing.T) {
	t.Parallel()

	files := deploymentFiles([]string{
		"docker-compose.yml",
		"./compose.override.yml",
		"/tmc-proxy/docker-compose.yml",
		"docker-compose.yml",
	})

	require.Equal(t, []string{
		"docker-compose.yml",
		"compose.override.yml",
		"tmc-proxy/docker-compose.yml",
		".env",
		"portainer.yml",
		"portainer.yaml",
	}, files)
}

func TestFinalizeDeploymentFilesArchivesAfterSuccessfulDeploy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := deploymentFiles([]string{"docker-compose.yml", "compose.override.yml"})
	writeDeploymentTestFiles(t, root)
	writeTestFile(t, root, "compose.override.yml", "services:\n  app:\n    environment: []\n")

	require.NoError(t, finalizeDeploymentFiles(root, ".deployed", files, false))

	require.NoFileExists(t, filepath.Join(root, "docker-compose.yml"))
	require.NoFileExists(t, filepath.Join(root, "compose.override.yml"))
	require.NoFileExists(t, filepath.Join(root, ".env"))
	require.NoFileExists(t, filepath.Join(root, "portainer.yml"))
	require.NoFileExists(t, filepath.Join(root, "portainer.yaml"))
	require.Equal(t, "services:\n  app:\n    image: alpine\n", readTestFile(t, root, ".deployed/docker-compose.yml"))
	require.Equal(t, "services:\n  app:\n    environment: []\n", readTestFile(t, root, ".deployed/compose.override.yml"))
	require.Equal(t, "VALUE=repo\n", readTestFile(t, root, ".deployed/.env"))
	require.Equal(t, "version: 1\n", readTestFile(t, root, ".deployed/portainer.yml"))
	require.Equal(t, "version: 1\n", readTestFile(t, root, ".deployed/portainer.yaml"))
	require.Equal(t, "http:\n  routers: {}\n", readTestFile(t, root, "traefik/dynamic/test.yaml"))
}

func TestFinalizeDeploymentFilesCleanupDeletesAfterSuccessfulDeploy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := deploymentFiles([]string{"docker-compose.yml"})
	writeDeploymentTestFiles(t, root)

	require.NoError(t, finalizeDeploymentFiles(root, ".deployed", files, true))

	require.NoFileExists(t, filepath.Join(root, "docker-compose.yml"))
	require.NoFileExists(t, filepath.Join(root, ".env"))
	require.NoFileExists(t, filepath.Join(root, "portainer.yml"))
	require.NoFileExists(t, filepath.Join(root, "portainer.yaml"))
	require.NoDirExists(t, filepath.Join(root, ".deployed"))
	require.Equal(t, "http:\n  routers: {}\n", readTestFile(t, root, "traefik/dynamic/test.yaml"))
}

func TestFinalizeDeploymentFilesFailedDeployLeavesRootFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := deploymentFiles([]string{"docker-compose.yml"})
	writeDeploymentTestFiles(t, root)
	deployErr := errors.New("deploy failed")

	err := finalizeDeploymentFilesAfterDeploy(root, ".deployed", files, false, deployErr)

	require.ErrorIs(t, err, deployErr)
	require.Equal(t, "services:\n  app:\n    image: alpine\n", readTestFile(t, root, "docker-compose.yml"))
	require.Equal(t, "VALUE=repo\n", readTestFile(t, root, ".env"))
	require.Equal(t, "version: 1\n", readTestFile(t, root, "portainer.yml"))
	require.Equal(t, "version: 1\n", readTestFile(t, root, "portainer.yaml"))
	require.NoDirExists(t, filepath.Join(root, ".deployed"))
}

func TestRestoreDeploymentFilesOnlyRestoresMissingRootFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := deploymentFiles([]string{"docker-compose.yml"})
	writeTestFile(t, root, ".deployed/docker-compose.yml", "services:\n  app:\n    image: alpine\n")
	writeTestFile(t, root, ".deployed/.env", "VALUE=archived\n")
	writeTestFile(t, root, ".env", "VALUE=local\n")

	require.NoError(t, restoreDeploymentFiles(root, ".deployed", files))

	require.Equal(t, "services:\n  app:\n    image: alpine\n", readTestFile(t, root, "docker-compose.yml"))
	require.Equal(t, "VALUE=local\n", readTestFile(t, root, ".env"))
	require.NoFileExists(t, filepath.Join(root, ".deployed/docker-compose.yml"))
	require.Equal(t, "VALUE=archived\n", readTestFile(t, root, ".deployed/.env"))
}

func TestArchiveDeploymentFilesDoesNotOverwriteModifiedArchiveTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := deploymentFiles([]string{"docker-compose.yml"})
	writeTestFile(t, root, "docker-compose.yml", "root\n")
	writeTestFile(t, root, ".deployed/docker-compose.yml", "archive-local\n")

	require.NoError(t, finalizeDeploymentFiles(root, ".deployed", files, false))

	require.Equal(t, "root\n", readTestFile(t, root, "docker-compose.yml"))
	require.Equal(t, "archive-local\n", readTestFile(t, root, ".deployed/docker-compose.yml"))
}

func TestArchiveDeploymentFilesRemovesRootWhenArchiveTargetIsIdentical(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := deploymentFiles([]string{"docker-compose.yml"})
	writeTestFile(t, root, "docker-compose.yml", "same\n")
	writeTestFile(t, root, ".deployed/docker-compose.yml", "same\n")

	require.NoError(t, finalizeDeploymentFiles(root, ".deployed", files, false))

	require.NoFileExists(t, filepath.Join(root, "docker-compose.yml"))
	require.Equal(t, "same\n", readTestFile(t, root, ".deployed/docker-compose.yml"))
}

func writeDeploymentTestFiles(t *testing.T, root string) {
	t.Helper()

	writeTestFile(t, root, "docker-compose.yml", "services:\n  app:\n    image: alpine\n")
	writeTestFile(t, root, ".env", "VALUE=repo\n")
	writeTestFile(t, root, "portainer.yml", "version: 1\n")
	writeTestFile(t, root, "portainer.yaml", "version: 1\n")
	writeTestFile(t, root, "traefik/dynamic/test.yaml", "http:\n  routers: {}\n")
}
