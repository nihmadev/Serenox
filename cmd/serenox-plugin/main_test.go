package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nihmadev/Serenox/internal/cli/plugincommands"
)

func TestCompatibilityHelpNamesBinary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := plugincommands.RunAs(context.Background(), "serenox-plugin", []string{"help"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage: serenox-plugin") {
		t.Fatalf("help = %q", stdout.String())
	}
}
