package plugin

import (
	"os"
	"strings"
	"testing"
)

func TestChildEnvironmentDoesNotInheritAmbientSecrets(t *testing.T) {
	t.Setenv("SERENOX_TEST_SECRET", "must-not-cross-process-boundary")
	environment := childEnvironment([]string{"EXPLICIT_SETTING=ok"}, map[string]string{
		envSocket: "socket", envToken: "token", envInstanceID: "instance", envProtocol: "1.0", envMaxFrameSize: "1024",
	})
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "SERENOX_TEST_SECRET") {
		t.Fatal("ambient secret was inherited by plugin environment")
	}
	if !strings.Contains(joined, "EXPLICIT_SETTING=ok") || os.Getenv("PATH") != "" && !strings.Contains(joined, "PATH=") {
		t.Fatalf("safe/explicit environment = %q", joined)
	}
}
