package commands

import (
	"fmt"
	"strings"

	"github.com/portainer/compose-unpacker/exec"
	"github.com/portainer/portainer/api/filesystem"
	"github.com/portainer/portainer/pkg/libstack/swarm"

	"github.com/rs/zerolog/log"
)

// SwarmDeployCommand contains the CLI input for deploying a Docker Swarm stack.
// It mirrors DeployCommand but adds Swarm-specific options such as Pull.
type SwarmDeployCommand struct {
	User                     string   `help:"Username for Git authentication." short:"u"`
	Password                 string   `help:"Password or PAT for Git authentication" short:"p"`
	Pull                     bool     `help:"Pull Image" short:"f"`
	Prune                    bool     `help:"Prune services during deployment" short:"r"`
	Keep                     bool     `help:"Keep stack folder" short:"k"`
	Flat                     bool     `help:"Clone repository directly into destination instead of destination/stacks/project/repo." name:"flat"`
	SourceDir                string   `help:"Repository subdirectory to sync into destination." name:"source-dir"`
	DeploymentDir            string   `help:"Directory used to archive deployment files after successful deploy." name:"deployment-dir"`
	CleanupDeploymentFiles   bool     `help:"Remove deployment files after successful deploy instead of archiving them." name:"cleanup-deployment-files"`
	SkipTLSVerify            bool     `help:"Skip TLS verification for git" name:"skip-tls-verify"`
	ForceRecreateStack       bool     `help:"Force to recreate the target stack regardless whether the image hash changes" name:"force-recreate"`
	Env                      []string `help:"OS ENV for stack."`
	Registry                 []string `help:"Registry credentials" name:"registry"`
	GitRepository            string   `arg:"" help:"Git repository to deploy from." name:"git-repo"`
	Reference                string   `arg:"" help:"Reference of Git repository to deploy from." name:"git-ref"`
	ProjectName              string   `arg:"" help:"Name of the Swarm stack." name:"project-name"`
	Destination              string   `arg:"" help:"Path on disk where the Git repository will be cloned." type:"path" name:"destination"`
	ComposeRelativeFilePaths []string `arg:"" help:"Relative path to the Compose file."  name:"compose-file-paths"`
}

// Run prepares a repository and deploys its files as a Docker Swarm stack.
func (cmd *SwarmDeployCommand) Run(cmdCtx *exec.CommandExecutionContext) error {
	log.Info().
		Str("repository", cmd.GitRepository).
		Strs("composePath", cmd.ComposeRelativeFilePaths).
		Str("destination", cmd.Destination).
		Msg("Deploying Swarm stack from a Git repository")

	if cmd.User != "" && cmd.Password != "" {
		log.Info().
			Str("user", cmd.User).
			Msg("Using Git authentication")
	}

	// The final URL segment becomes the repository directory name.
	i := strings.LastIndex(cmd.GitRepository, "/")
	if i == -1 {
		log.Error().
			Str("repository", cmd.GitRepository).
			Msg("Invalid Git repository URL")

		return exec.ErrDeployComposeFailure
	}
	repositoryName := strings.TrimSuffix(cmd.GitRepository[i+1:], ".git")

	log.Info().
		Str("directory", cmd.Destination).
		Msg("Checking the file system...")

	if cmd.Flat {
		if err := validateFlatDestination(cmd.Destination); err != nil {
			log.Error().Err(err).Msg("Invalid flat destination")
			return err
		}
	}

	deploymentDir, err := cleanDeploymentDir(cmd.DeploymentDir)
	if err != nil {
		log.Error().Err(err).Msg("Invalid deployment directory")
		return err
	}

	mountPath, clonePath := gitRepositoryDeploymentPaths(cmd.Destination, cmd.ProjectName, repositoryName, cmd.Flat)
	// Source-dir content moves to the destination root, so adjust file paths too.
	composeRelativeFilePaths := make([]string, len(cmd.ComposeRelativeFilePaths))
	for i := range len(cmd.ComposeRelativeFilePaths) {
		composeRelativeFilePath, err := stripRepositorySourceDir(cmd.ComposeRelativeFilePaths[i], cmd.SourceDir)
		if err != nil {
			log.Error().Err(err).Msg("Invalid Compose file path")
			return err
		}

		composeRelativeFilePaths[i] = composeRelativeFilePath
	}

	// Deployment-file archiving is intentionally limited to flat source-dir mode.
	manageDeploymentFiles := cmd.Flat && strings.TrimSpace(cmd.SourceDir) != "" && (deploymentDir != "" || cmd.CleanupDeploymentFiles)
	deploymentFilePaths := deploymentFiles(composeRelativeFilePaths)
	var unprotectMissingPaths map[string]struct{}
	if manageDeploymentFiles {
		unprotectMissingPaths = deploymentFileSet(deploymentFilePaths)
	}
	if manageDeploymentFiles && deploymentDir != "" {
		if err := restoreDeploymentFiles(clonePath, deploymentDir, deploymentFilePaths); err != nil {
			log.Error().Err(err).Msg("Failed to restore archived deployment files")
			return err
		}
	}

	// Prepare the requested Git content before contacting Docker Swarm.
	if err := prepareGitRepository(cmdCtx, gitRepositoryOptions{
		repository:            cmd.GitRepository,
		reference:             cmd.Reference,
		user:                  cmd.User,
		password:              cmd.Password,
		skipTLSVerify:         cmd.SkipTLSVerify,
		keep:                  cmd.Keep,
		mountPath:             mountPath,
		clonePath:             clonePath,
		sourceDir:             cmd.SourceDir,
		unprotectMissingPaths: unprotectMissingPaths,
	}); err != nil {
		return err
	}

	// The Portainer library performs the actual Docker Swarm operation.
	deployer := swarm.NewSwarmDeployer()

	composeFilePaths := make([]string, len(composeRelativeFilePaths))
	for i := range len(composeRelativeFilePaths) {
		composeFilePaths[i] = filesystem.JoinPaths(clonePath, composeRelativeFilePaths[i])
	}

	registries := exec.ParseRegistryCredentials(cmd.Registry)

	log.Info().
		Strs("composeFilePaths", composeFilePaths).
		Str("workingDirectory", clonePath).
		Str("projectName", cmd.ProjectName).
		Msg("Deploying Swarm stack")

	if err := deployer.Deploy(cmdCtx.Context, composeFilePaths, swarm.DeployOptions{
		Options: swarm.Options{
			WorkingDir:  clonePath,
			ProjectName: cmd.ProjectName,
			Env:         cmd.Env,
			Registries:  registries,
		},
		RemoveOrphans: cmd.Prune,
		PullImage:     cmd.Pull,
		ForceRecreate: cmd.ForceRecreateStack,
	}); err != nil {
		log.Error().
			Err(err).
			Msg("Failed to deploy Swarm stack")
		return fmt.Errorf("%w: %w", exec.ErrDeployComposeFailure, err)
	}

	// Only archive or delete deployment files after Docker reports success.
	if manageDeploymentFiles {
		if err := finalizeDeploymentFilesAfterDeploy(clonePath, deploymentDir, deploymentFilePaths, cmd.CleanupDeploymentFiles, nil); err != nil {
			log.Error().Err(err).Msg("Failed to finalize deployment files")
			return fmt.Errorf("%w: %w", exec.ErrDeployComposeFailure, err)
		}
	}

	log.Info().Msg("Swarm stack deployment complete")
	return nil
}
