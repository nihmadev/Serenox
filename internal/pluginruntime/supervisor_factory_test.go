package pluginruntime

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nihmadev/Serenox/internal/plugin"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestSupervisorFactoryMapsNativeEntrypointToBoundedConfig(t *testing.T) {
	builder := &capturingSupervisorConstructor{metadata: metadata("alpha", "1.0.0")}
	factory, err := NewSupervisorFactory(SupervisorFactoryConfig{
		Template: plugin.SupervisorConfig{
			Command: "ignored", Executable: "also-ignored", Args: []string{"--native"},
			Dir: "/ignored", ExpectedPluginID: "ignored", InstanceID: "shared-is-unsafe",
			MaxInFlight: 3, OutputLimitBytes: 4096,
		},
		Constructor: builder.build,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := supervisorSpec("alpha", "1.0.0", pluginpackage.CategoryGoExecutable)
	process, err := factory.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	config := builder.configs[0]
	if config.Command != spec.Entrypoint || config.Executable != "" || config.Dir != spec.VersionRoot ||
		config.ExpectedPluginID != spec.ID || config.InstanceID != "" {
		t.Fatalf("native supervisor config = %#v", config)
	}
	if !reflect.DeepEqual(config.Args, []string{"--native"}) || config.MaxInFlight != 3 || config.OutputLimitBytes != 4096 {
		t.Fatalf("native args/limits = %#v", config)
	}
	if process.Metadata().ID != "alpha" {
		t.Fatalf("process metadata = %#v", process.Metadata())
	}
	if err := process.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorFactoryWiresHostHandlersForNativePlugins(t *testing.T) {
	builder := &capturingSupervisorConstructor{metadata: metadata("media", "1.0.0")}
	handlerCalled := false
	factory, err := NewSupervisorFactory(SupervisorFactoryConfig{
		MediaHandler: func(context.Context, string, int64, pluginv1.MediaRequest) (pluginv1.MediaResult, error) {
			handlerCalled = true
			return pluginv1.MediaResult{Media: &pluginv1.Media{Kind: "photo", Handle: "m1_test"}}, nil
		},
		Constructor: builder.build,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := supervisorSpec("media", "1.0.0", pluginpackage.CategoryGoExecutable)
	spec.Permissions = []string{"media.read"}
	if _, err := factory.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	config := builder.configs[0]
	if config.MediaHandler == nil {
		t.Fatal("native plugin media handler was not wired")
	}
	if _, err := config.MediaHandler(context.Background(), "media", 1, pluginv1.MediaRequest{Operation: pluginv1.MediaInspect, Handle: "m1_test"}); err != nil {
		t.Fatalf("native plugin media handler: %v", err)
	}
	if !handlerCalled {
		t.Fatal("native plugin media handler was not called")
	}
}

func TestSupervisorFactoryMapsPythonPackageToSidecar(t *testing.T) {
	builder := &capturingSupervisorConstructor{metadata: metadata("legacy", "2.0.0")}
	factory, err := NewSupervisorFactory(SupervisorFactoryConfig{
		Template:          plugin.SupervisorConfig{Args: []string{"ignored-native-arg"}, MaxInFlight: 2},
		EnablePython:      true,
		PythonExecutable:  "/opt/serenox/python",
		PythonSidecarArgs: []string{"-m", "serenox_sidecar", "--workers", "2"},
		Constructor:       builder.build,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := supervisorSpec("legacy", "2.0.0", pluginpackage.CategoryPythonSidecar)
	spec.Capabilities = []string{string(pluginv1.CapabilityCommands)}
	process, err := factory.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	config := builder.configs[0]
	if config.Command != "/opt/serenox/python" || config.Dir != spec.VersionRoot || config.ExpectedPluginID != "legacy" {
		t.Fatalf("Python supervisor config = %#v", config)
	}
	wantArgs := []string{"-m", "serenox_sidecar", "--workers", "2", "--capabilities", "commands", "--module", spec.Entrypoint}
	if !reflect.DeepEqual(config.Args, wantArgs) {
		t.Fatalf("Python args = %q, want %q", config.Args, wantArgs)
	}
	if err := process.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorFactoryKeepsPythonDisabledByDefault(t *testing.T) {
	called := false
	factory, err := NewSupervisorFactory(SupervisorFactoryConfig{
		Constructor: func(plugin.SupervisorConfig) (SupervisedProcess, error) {
			called = true
			return nil, errors.New("must not be called")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = factory.Start(context.Background(), supervisorSpec("legacy", "1.0.0", pluginpackage.CategoryPythonSidecar))
	if err == nil || !strings.Contains(err.Error(), "Python sidecar support is disabled") {
		t.Fatalf("Start error = %v", err)
	}
	if called {
		t.Fatal("constructor called while Python support was disabled")
	}
}

func TestSupervisorFactoryCleansFailedStartAndRejectsUnknownCategory(t *testing.T) {
	startFailure := errors.New("handshake failed")
	builder := &capturingSupervisorConstructor{metadata: metadata("alpha", "1.0.0"), startErr: startFailure}
	factory, err := NewSupervisorFactory(SupervisorFactoryConfig{Constructor: builder.build})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Start(context.Background(), supervisorSpec("alpha", "1.0.0", pluginpackage.CategoryGoExecutable)); !errors.Is(err, startFailure) {
		t.Fatalf("Start error = %v", err)
	}
	if builder.process == nil || builder.process.stops != 1 {
		t.Fatalf("failed supervisor stop count = %#v", builder.process)
	}
	unknown := supervisorSpec("alpha", "1.0.0", pluginpackage.Category("wasm"))
	if _, err := factory.Start(context.Background(), unknown); err == nil {
		t.Fatal("unknown category was accepted")
	}
	if len(builder.configs) != 1 {
		t.Fatalf("constructor called for unknown category: %d", len(builder.configs))
	}
}

func supervisorSpec(id, version string, category pluginpackage.Category) LaunchSpec {
	root := filepath.Join("/verified", id, version)
	entrypoint := filepath.Join(root, "bin", "plugin")
	if category == pluginpackage.CategoryPythonSidecar {
		entrypoint = filepath.Join(root, "plugin.py")
	}
	return LaunchSpec{ID: id, Name: id, Version: version, Category: category, VersionRoot: root, Entrypoint: entrypoint}
}

func metadata(id, version string) pluginv1.PluginMetadata {
	return pluginv1.PluginMetadata{ID: id, Name: id, Version: version, ProtocolVersion: pluginv1.Version}
}

type capturingSupervisorConstructor struct {
	configs  []plugin.SupervisorConfig
	metadata pluginv1.PluginMetadata
	startErr error
	process  *fakeSupervisedProcess
}

func (b *capturingSupervisorConstructor) build(config plugin.SupervisorConfig) (SupervisedProcess, error) {
	b.configs = append(b.configs, config)
	b.process = &fakeSupervisedProcess{metadata: b.metadata, startErr: b.startErr}
	return b.process, nil
}

type fakeSupervisedProcess struct {
	metadata pluginv1.PluginMetadata
	startErr error
	stops    int
}

func (p *fakeSupervisedProcess) Start(context.Context, ...[]pluginv1.ConfigValue) error {
	return p.startErr
}

func (p *fakeSupervisedProcess) Metadata() (pluginv1.PluginMetadata, bool) {
	return p.metadata, p.metadata.ID != ""
}

func (p *fakeSupervisedProcess) Dispatch(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error) {
	return pluginv1.DispatchResponse{}, nil
}

func (p *fakeSupervisedProcess) Wait(context.Context) error { return nil }

func (p *fakeSupervisedProcess) Stop(context.Context) error {
	p.stops++
	return nil
}
