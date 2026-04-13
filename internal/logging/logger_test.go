package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewFiltersByLevel(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := New(&buffer, Config{
		Level:  LevelInfo,
		Format: FormatText,
	})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	logger.Debug("hidden")
	logger.Info("shown", "component", "hub")

	output := buffer.String()
	if strings.Contains(output, "hidden") {
		t.Fatalf("debug log should have been filtered: %s", output)
	}
	if !strings.Contains(output, "shown") {
		t.Fatalf("missing info log: %s", output)
	}
	if !strings.Contains(output, "component=hub") {
		t.Fatalf("missing structured field: %s", output)
	}
}

func TestNewSupportsJSONFormat(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := New(&buffer, Config{
		Level:  LevelDebug,
		Format: FormatJSON,
	})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	logger.Info("shown", "component", "edge")

	output := buffer.String()
	if !strings.Contains(output, `"msg":"shown"`) {
		t.Fatalf("missing json message: %s", output)
	}
	if !strings.Contains(output, `"component":"edge"`) {
		t.Fatalf("missing json field: %s", output)
	}
	if !strings.Contains(output, `"level":"info"`) {
		t.Fatalf("missing normalized json level: %s", output)
	}
}
