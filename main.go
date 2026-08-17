package main

import (
	"context"
	"fmt"
	"os"

	"github.com/portainer/compose-unpacker/commands"
	"github.com/portainer/compose-unpacker/exec"
	"github.com/portainer/compose-unpacker/log"
	"github.com/portainer/portainer/pkg/fips"

	"github.com/alecthomas/kong"
)

// UNPACKER_EXIT_ERROR is returned to the operating system when a command fails.
const UNPACKER_EXIT_ERROR = 255

// cli describes the entire command-line interface. Kong reads the text inside
// backticks (struct tags), much like a Java library reads annotations.
var cli struct {
	LogLevel      log.Level                     `kong:"help='Set the logging level',default='INFO',enum='DEBUG,INFO,WARN,ERROR',env='LOG_LEVEL'"`
	PrettyLog     bool                          `kong:"help='Whether to enable or disable colored logs output',default='false',env='PRETTY_LOG'"`
	Deploy        commands.DeployCommand        `cmd:"" help:"Deploy a stack from a Git repository."`
	Undeploy      commands.UndeployCommand      `cmd:"" help:"Remove a stack from a Git repository."`
	SwarmDeploy   commands.SwarmDeployCommand   `cmd:"" help:"Deploy a Swarm stack from a Git repository."`
	SwarmUndeploy commands.SwarmUndeployCommand `cmd:"" help:"Remove a Swarm stack from a Git repository."`
	RemoveDir     commands.RemoveDirCommand     `cmd:"" help:"Remove a directory."`
	FipsMode      bool                          `kong:"help='Start in FIPS mode',name='fips-mode',env='FIPS_MODE',default:'false'"`
}

func main() {
	// A context is passed down to Git and Docker operations so they can support
	// cancellation. context.Background is the root context for this CLI process.
	ctx := context.Background()

	// Kong fills cli from os.Args and returns the context for the selected command.
	cliCtx := kong.Parse(&cli,
		kong.Name("unpacker"),
		kong.Description("A tool to deploy Docker stacks from Git repositories."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
			Summary: true,
		}))

	// Configure process-wide features before running the selected command.
	fips.InitFIPS(cli.FipsMode)

	log.ConfigureLogger(cli.PrettyLog)
	log.SetLoggingLevel(cli.LogLevel)

	// Every command has a Run method that accepts this shared context. Kong calls
	// the Run method belonging to the subcommand chosen by the user.
	cmdCtx := exec.NewCommandExecutionContext(ctx)
	if err := cliCtx.Run(cmdCtx); err != nil {
		fmt.Println(err)
		os.Exit(UNPACKER_EXIT_ERROR)
	}
}
