package exec

import (
	"strings"

	"github.com/portainer/portainer/api/filesystem"

	"github.com/docker/cli/cli/config/types"
	"github.com/rs/zerolog/log"
)

// MakeWorkingDir builds the directory that belongs to one stack.
func MakeWorkingDir(target, stackName string) string {
	// The filesystem helper joins paths correctly on both Linux and Windows.
	return filesystem.JoinPaths(target, "stacks", stackName)
}

// ParseRegistryCredentials converts CLI strings into Docker authentication
// objects. Accepted forms are user:password:server and user:password:host:port.
func ParseRegistryCredentials(raw []string) []types.AuthConfig {
	// A slice is Go's flexible array type, similar to an ArrayList in Java.
	var registries []types.AuthConfig
	for _, r := range raw {
		credentials := strings.Split(r, ":")
		partsLen := len(credentials)
		if partsLen != 3 && partsLen != 4 {
			log.Warn().
				Str("registry", r).
				Msg("Registry is malformed, skipping login")
			continue
		}

		serverAddr := credentials[2]
		if partsLen == 4 {
			serverAddr += ":" + credentials[3]
		}

		// append returns the updated slice because its backing array may grow.
		registries = append(registries, types.AuthConfig{
			Username:      credentials[0],
			Password:      credentials[1],
			ServerAddress: serverAddr,
		})
	}
	return registries
}
