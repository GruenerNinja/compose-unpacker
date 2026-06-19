package commands

import (
	"fmt"
	"strings"

	"github.com/portainer/compose-unpacker/exec"
	"github.com/portainer/portainer/api/filesystem"
	"github.com/portainer/portainer/pkg/libstack/swarm"

	"github.com/rs/zerolog/log"
)

type SwarmDeployCommand struct {
	User                     string   `help:"Username for Git authentication." short:"u"`
	Password                 string   `help:"Password or PAT for Git authentication" short:"p"`
	Pull                     bool     `help:"Pull Image" short:"f"`
	Prune                    bool     `help:"Prune services during deployment" short:"r"`
	Keep                     bool     `help:"Keep stack folder" short:"k"`
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

	mountPath := exec.MakeWorkingDir(cmd.Destination, cmd.ProjectName)
	clonePath := filesystem.JoinPaths(mountPath, repositoryName)

	if err := prepareGitRepository(cmdCtx, gitRepositoryOptions{
		repository:    cmd.GitRepository,
		reference:     cmd.Reference,
		user:          cmd.User,
		password:      cmd.Password,
		skipTLSVerify: cmd.SkipTLSVerify,
		keep:          cmd.Keep,
		mountPath:     mountPath,
		clonePath:     clonePath,
	}); err != nil {
		return err
	}

	deployer := swarm.NewSwarmDeployer()

	composeFilePaths := make([]string, len(cmd.ComposeRelativeFilePaths))
	for i := range len(cmd.ComposeRelativeFilePaths) {
		composeFilePaths[i] = filesystem.JoinPaths(clonePath, cmd.ComposeRelativeFilePaths[i])
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

	log.Info().Msg("Swarm stack deployment complete")
	return nil
}
