package logging

import (
	"errors"
	"io"
	"log/slog"
	"strings"
)

const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"

	FormatJSON = "json"
	FormatText = "text"
)

type Config struct {
	Level  string
	Format string
}

func New(writer io.Writer, cfg Config) (*slog.Logger, error) {
	if writer == nil {
		writer = io.Discard
	}

	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	format, err := ParseFormat(cfg.Format)
	if err != nil {
		return nil, err
	}

	options := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key != slog.LevelKey {
				return attr
			}

			attr.Value = slog.StringValue(strings.ToLower(attr.Value.String()))
			return attr
		},
	}

	switch format {
	case FormatJSON:
		return slog.New(slog.NewJSONHandler(writer, options)), nil
	case FormatText:
		return slog.New(slog.NewTextHandler(writer, options)), nil
	default:
		return nil, errors.New("unsupported log format")
	}
}

func ParseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", LevelInfo:
		return slog.LevelInfo, nil
	case LevelDebug:
		return slog.LevelDebug, nil
	case LevelWarn:
		return slog.LevelWarn, nil
	case LevelError:
		return slog.LevelError, nil
	default:
		return 0, errors.New("invalid log level")
	}
}

func ParseFormat(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", FormatJSON:
		return FormatJSON, nil
	case FormatText:
		return FormatText, nil
	default:
		return "", errors.New("invalid log format")
	}
}

func Discard() *slog.Logger {
	logger, _ := New(io.Discard, Config{Level: LevelInfo, Format: FormatText})
	return logger
}
