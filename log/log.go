package log

import (
	stdlog "log"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog/pkgerrors"
)

// Level is the logging severity accepted by the command-line interface.
type Level string

const (
	// ErrorLevel level. Logs. Used for errors that should definitely be noted.
	LevelError Level = "ERROR"
	// WarnLevel level. Non-critical entries that deserve eyes.
	LevelWarn Level = "WARN"
	// InfoLevel level. General operational entries about what's going on inside the
	// application.
	LevelInfo Level = "INFO"
	// DebugLevel level. Usually only enabled when debugging. Very verbose logging.
	LevelDebug Level = "DEBUG"
)

var (
	// mapLevel translates this project's CLI values into zerolog's values.
	mapLevel = map[Level]zerolog.Level{
		LevelError: zerolog.ErrorLevel,
		LevelWarn:  zerolog.WarnLevel,
		LevelInfo:  zerolog.InfoLevel,
		LevelDebug: zerolog.DebugLevel,
	}
)

// ConfigureLogger configures the process-wide logger. pretty selects readable
// console output; the default is structured JSON output.
func ConfigureLogger(pretty bool) {
	zerolog.ErrorStackFieldName = "stack_trace"
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	stdlog.SetFlags(0)
	stdlog.SetOutput(log.Logger)

	log.Logger = log.Logger.With().Caller().Stack().Logger()

	if pretty {
		log.Logger = log.Logger.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}
}

// SetLoggingLevel hides messages below the requested severity for the process.
func SetLoggingLevel(level Level) {
	zerolog.SetGlobalLevel(mapLevel[level])
}
