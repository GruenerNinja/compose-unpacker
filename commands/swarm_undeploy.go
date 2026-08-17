package commands

import (
	"os"

	"github.com/portainer/compose-unpacker/exec"
	"github.com/portainer/portainer/pkg/libstack/swarm"
	"github.com/rs/zerolog/log"
)

// SwarmUndeployCommand contains the CLI input for removing a Swarm stack.
type SwarmUndeployCommand struct {
	Keep        bool   `help:"Keep stack folder" short:"k"`
	Flat        bool   `help:"Clone repository directly into destination instead of destination/stacks/project/repo." name:"flat"`
	ProjectName string `arg:"" help:"Name of the Compose (Swarm) stack." name:"project-name"`
	Destination string `arg:"" help:"Path on disk where the Git repository will be cloned." type:"path" name:"destination"`
}

// Run removes a Swarm stack and optionally removes its local working files.
func (cmd *SwarmUndeployCommand) Run(cmdCtx *exec.CommandExecutionContext) error {
	log.Info().
		Str("stack_name", cmd.ProjectName).
		Str("destination", cmd.Destination).
		Msg("Undeploying Swarm stack from Git repository")

	// The Portainer deployer translates this call into Docker Swarm operations.
	deployer := swarm.NewSwarmDeployer()

	if err := deployer.Remove(cmdCtx.Context, cmd.ProjectName, swarm.RemoveOptions{}); err != nil {
		log.Error().
			Err(err).
			Msg("Failed to remove Swarm stack")
		return err
	}

	if cmd.Flat {
		if err := validateFlatDestination(cmd.Destination); err != nil {
			log.Error().Err(err).Msg("Invalid flat destination")
			return err
		}
	}

	mountPath := gitRepositoryMountPath(cmd.Destination, cmd.ProjectName, cmd.Flat)
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
