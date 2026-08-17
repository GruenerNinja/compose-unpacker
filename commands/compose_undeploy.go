package commands

import (
	"os"
	"strings"

	"github.com/portainer/compose-unpacker/exec"
	"github.com/portainer/portainer/pkg/libstack"
	"github.com/portainer/portainer/pkg/libstack/compose"
	"github.com/rs/zerolog/log"
)

// UndeployCommand contains the CLI input for removing a Compose stack.
type UndeployCommand struct {
	User          string `help:"Username for Git authentication." short:"u"`
	Password      string `help:"Password or PAT for Git authentication" short:"p"`
	Keep          bool   `help:"Keep stack folder" short:"k"`
	Flat          bool   `help:"Clone repository directly into destination instead of destination/stacks/project/repo." name:"flat"`
	RemoveVolumes bool   `help:"Remove volumes" short:"v"`

	GitRepository            string   `arg:"" help:"Git repository to deploy from." name:"git-repo"`
	ProjectName              string   `arg:"" help:"Name of the Compose stack." name:"project-name"`
	Destination              string   `arg:"" help:"Path on disk where the Git repository will be cloned." type:"path" name:"destination"`
	ComposeRelativeFilePaths []string `arg:"" help:"Relative path to the Compose file." name:"compose-file-path"`
}

// Run removes a Compose stack and optionally removes its local working files.
func (cmd *UndeployCommand) Run(cmdCtx *exec.CommandExecutionContext) error {
	log.Info().
		Str("repository", cmd.GitRepository).
		Strs("compose_path", cmd.ComposeRelativeFilePaths).
		Msg("Undeploying Compose stack from Git repository")

	if strings.LastIndex(cmd.GitRepository, "/") == -1 {
		log.Error().
			Str("repository", cmd.GitRepository).
			Msg("Invalid Git repository URL")

		return exec.ErrDeployComposeFailure
	}

	if cmd.Flat {
		if err := validateFlatDestination(cmd.Destination); err != nil {
			log.Error().Err(err).Msg("Invalid flat destination")
			return err
		}
	}

	mountPath := gitRepositoryMountPath(cmd.Destination, cmd.ProjectName, cmd.Flat)

	// Removal talks to Docker by project name; it does not need to clone Git again.
	deployer := compose.NewComposeDeployer()

	log.Debug().
		Str("project_name", cmd.ProjectName).
		Bool("remove_volumes", cmd.RemoveVolumes).
		Msg("Undeploying Compose stack")

	if err := deployer.Remove(cmdCtx.Context, cmd.ProjectName, nil, libstack.RemoveOptions{Volumes: cmd.RemoveVolumes}); err != nil {
		log.Error().
			Err(err).
			Msg("Failed to remove Compose stack")
		return exec.ErrDeployComposeFailure
	}

	log.Info().Msg("Compose stack remove complete")

	// Keep controls local files only. The Docker stack has already been removed.
	if !cmd.Keep {
		if err := os.RemoveAll(mountPath); err != nil {
			log.Error().
				Err(err).
				Msg("Failed to remove Compose stack project folder")
		}
	}

	return nil
}
