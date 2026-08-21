// Command time-plugin-e2e verifies the complete local external-plugin path
// without importing or contacting the Telegram adapter.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nihmadev/Serenox/internal/cli/plugincommands"
	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/plugin"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
	"github.com/nihmadev/Serenox/internal/pluginruntime"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	pluginID            = "example.time"
	pluginVersion       = "1.0.0"
	timePrefix          = "Current time: "
	e2eSelfID     int64 = 9001
)

type options struct {
	source   string
	manifest string
	archive  string
	data     string
}

type readyPlugin struct {
	metadata protocol.PluginMetadata
	dispatch pluginruntime.DispatchFunc
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "TIME PLUGIN E2E FAIL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var cfg options
	flag.StringVar(&cfg.source, "source", "", "prepared package payload directory")
	flag.StringVar(&cfg.manifest, "manifest", "", "manifest v1 template")
	flag.StringVar(&cfg.archive, "archive", "", "output .srnx path")
	flag.StringVar(&cfg.data, "data", "", "isolated installation root")
	flag.Parse()
	if flag.NArg() != 0 || cfg.source == "" || cfg.manifest == "" || cfg.archive == "" || cfg.data == "" {
		return errors.New("--source, --manifest, --archive, and --data are required")
	}
	for _, path := range []*string{&cfg.source, &cfg.manifest, &cfg.archive, &cfg.data} {
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		*path = absolute
	}
	if !strings.EqualFold(filepath.Ext(cfg.archive), ".srnx") {
		return errors.New("archive must use the .srnx extension")
	}

	rootCtx, cancelRoot := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRoot()
	if err := os.MkdirAll(cfg.data, 0o700); err != nil {
		return fmt.Errorf("create data root: %w", err)
	}
	runtimeRoot := filepath.Join(cfg.data, "runtime")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		return fmt.Errorf("create runtime root: %w", err)
	}

	packCtx, cancelPack := context.WithTimeout(rootCtx, 8*time.Second)
	packOutput, err := runPluginCommand(packCtx, "pack",
		"--source", cfg.source,
		"--manifest", cfg.manifest,
		"--output", cfg.archive,
	)
	cancelPack()
	if err != nil {
		return err
	}
	fmt.Printf("CLI PACK: %s", packOutput)

	verifyOptions := pluginpackage.DefaultVerifyOptions()
	verifyCtx, cancelVerify := context.WithTimeout(rootCtx, 5*time.Second)
	verifyOutput, err := runPluginCommand(verifyCtx, "verify", cfg.archive)
	cancelVerify()
	if err != nil {
		return err
	}
	fmt.Printf("CLI VERIFY: %s", verifyOutput)

	inspectCtx, cancelInspect := context.WithTimeout(rootCtx, 5*time.Second)
	verified, err := pluginpackage.Verify(inspectCtx, cfg.archive, verifyOptions)
	cancelInspect()
	if err != nil {
		return fmt.Errorf("inspect verified archive: %w", err)
	}
	if verified.Manifest.Format != pluginpackage.FormatV1 || verified.Manifest.ID != pluginID || verified.Manifest.Version != pluginVersion {
		return fmt.Errorf("unexpected verified identity: %s %s@%s", verified.Manifest.Format, verified.Manifest.ID, verified.Manifest.Version)
	}
	fmt.Printf("VERIFY: %s digest=%s signed=%t entrypoint=%s\n", verified.Manifest.Format, verified.ManifestDigest, verified.Signed, verified.Entrypoint.Path)

	installCtx, cancelInstall := context.WithTimeout(rootCtx, 8*time.Second)
	installOutput, err := runPluginCommand(installCtx, "install", "--root", cfg.data, "--retain", "2", cfg.archive)
	cancelInstall()
	if err != nil {
		return err
	}
	fmt.Printf("CLI INSTALL: %s", installOutput)

	manager, err := pluginpackage.NewManager(pluginpackage.ManagerConfig{
		Root: cfg.data, Verify: verifyOptions, RetainVersions: 2,
	})
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	factory, err := pluginruntime.NewSupervisorFactory(pluginruntime.SupervisorFactoryConfig{
		Template: plugin.SupervisorConfig{
			HostVersion:      pluginpackage.DefaultCoreVersion,
			RuntimeBaseDir:   runtimeRoot,
			MaxFrameSize:     64 << 10,
			MaxInFlight:      2,
			HandshakeTimeout: 2 * time.Second,
			ConfigureTimeout: 2 * time.Second,
			StartTimeout:     2 * time.Second,
			HealthTimeout:    2 * time.Second,
			DispatchTimeout:  2 * time.Second,
			StopTimeout:      2 * time.Second,
			TerminateTimeout: time.Second,
			HealthInterval:   time.Minute,
			DisableHealth:    true,
			OutputLimitBytes: 8 << 10,
			Restart: plugin.RestartPolicy{
				Disabled: true, MaxRestarts: 1, Window: time.Minute,
				InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, DisableJitter: true,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("supervisor factory: %w", err)
	}

	commandRegistry, err := dispatch.NewCommandRegistry(dispatch.RegistryLimits{MaxCommands: 8, MaxAliases: 8})
	if err != nil {
		return err
	}
	ready := make(chan readyPlugin, 1)
	controller, err := pluginruntime.New(pluginruntime.Config{
		Manager: manager, Factory: factory, MaxPlugins: 1,
		StopTimeout: 2 * time.Second, RollbackTimeout: 3 * time.Second,
		Hooks: pluginruntime.Hooks{
			OnReady: func(metadata protocol.PluginMetadata, call pluginruntime.DispatchFunc) error {
				if metadata.ID != pluginID || metadata.Version != pluginVersion {
					return fmt.Errorf("unexpected RPC metadata %s@%s", metadata.ID, metadata.Version)
				}
				if len(metadata.Commands) != 1 || metadata.Commands[0].Name != "time" {
					return fmt.Errorf("RPC metadata did not declare command time: %#v", metadata.Commands)
				}
				command := metadata.Commands[0]
				if err := commandRegistry.Register(dispatch.CommandTarget{
					Name: command.Name, PluginID: metadata.ID, HandlerID: command.Name,
				}, command.Aliases...); err != nil {
					return err
				}
				select {
				case ready <- readyPlugin{metadata: metadata, dispatch: call}:
					return nil
				default:
					commandRegistry.UnregisterPlugin(metadata.ID)
					return errors.New("ready channel is full")
				}
			},
			OnStopped: commandRegistry.UnregisterPlugin,
		},
	})
	if err != nil {
		return fmt.Errorf("controller: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = controller.Close(closeCtx)
		cancel()
	}()

	loadCtx, cancelLoad := context.WithTimeout(rootCtx, 8*time.Second)
	err = controller.Restore(loadCtx)
	cancelLoad()
	if err != nil {
		return fmt.Errorf("restore installed plugin and start: %w", err)
	}
	var active readyPlugin
	select {
	case active = <-ready:
	case <-rootCtx.Done():
		return rootCtx.Err()
	}
	installed, err := manager.List(rootCtx)
	if err != nil {
		return fmt.Errorf("list installed: %w", err)
	}
	status := controller.List()
	if len(installed) != 1 || installed[0].ID != pluginID || installed[0].ActiveVersion != pluginVersion ||
		len(status) != 1 || status[0].ID != pluginID || status[0].State != "running" {
		return fmt.Errorf("unexpected installed/runtime state: installed=%#v runtime=%#v", installed, status)
	}
	installedPath := ""
	for _, version := range installed[0].Versions {
		if version.Active {
			installedPath = version.Path
		}
	}
	fmt.Printf("INSTALL: %s@%s active=%s state=%s root=%s\n", installed[0].ID, installed[0].ActiveVersion, installed[0].ActiveVersion, status[0].State, installedPath)
	fmt.Printf("RPC: metadata=%s@%s protocol=%s command=%s owner_only=%t\n",
		active.metadata.ID, active.metadata.Version, active.metadata.ProtocolVersion,
		active.metadata.Commands[0].Name, active.metadata.Commands[0].OwnerOnly)

	parser, err := dispatch.NewParser(".", e2eSelfID)
	if err != nil {
		return err
	}
	message := dispatch.Message{
		ID: 77, ChatID: -100123, SenderID: e2eSelfID, UpdateKind: string(protocol.UpdateMessage),
		Text: ".time", Outgoing: true, ChatKind: dispatch.ChatPrivate, MediaKind: dispatch.MediaNone,
	}
	parsed, ok := parser.Parse(message)
	if !ok {
		return errors.New("real command parser rejected .time")
	}
	target, ok := commandRegistry.Resolve(parsed.Name)
	if !ok || target.PluginID != pluginID || target.HandlerID != "time" {
		return fmt.Errorf("command registry did not resolve .time: %#v", target)
	}
	now := time.Now().UTC()
	dtoMessage := protocol.Message{
		ID: message.ID, ChatID: message.ChatID, SenderID: message.SenderID,
		DateUnixMS: now.UnixMilli(), Text: message.Text, Outgoing: message.Outgoing,
		ChatKind: string(message.ChatKind),
	}
	request := protocol.DispatchRequest{
		Update: protocol.Update{
			ID: "time-e2e-update", Kind: protocol.UpdateMessage,
			OccurredUnixMS: now.UnixMilli(), Message: &dtoMessage,
		},
		Command: &protocol.Command{
			Name: target.Name, Prefix: parsed.Prefix, RawArgs: parsed.Args,
			Args: strings.Fields(parsed.Args), Message: dtoMessage,
		},
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("parser-mapped request: %w", err)
	}
	fmt.Printf("PARSE: input=%q parsed_name=%s prefix=%q registry_plugin=%s\n", message.Text, parsed.Name, parsed.Prefix, target.PluginID)

	dispatchCtx, cancelDispatch := context.WithTimeout(rootCtx, 2*time.Second)
	before := time.Now().UTC()
	response, err := active.dispatch(dispatchCtx, request)
	after := time.Now().UTC()
	cancelDispatch()
	if err != nil {
		return fmt.Errorf("supervisor RPC dispatch: %w", err)
	}
	if len(response.Actions) != 1 {
		return fmt.Errorf("dispatch actions = %d, want 1", len(response.Actions))
	}
	action := response.Actions[0]
	if action.Kind != protocol.ActionReply || action.ChatID != message.ChatID || action.ReplyToMessageID != message.ID {
		return fmt.Errorf("unexpected reply action: %#v", action)
	}
	stampText, found := strings.CutPrefix(action.Text, timePrefix)
	if !found {
		return fmt.Errorf("action text %q has no current-time prefix", action.Text)
	}
	stamp, err := time.Parse(time.RFC3339Nano, stampText)
	if err != nil {
		return fmt.Errorf("parse action timestamp %q: %w", stampText, err)
	}
	if stamp.Before(before) || stamp.After(after) {
		return fmt.Errorf("action timestamp %s is outside dispatch window [%s, %s]", stamp, before, after)
	}
	fmt.Printf("DISPATCH: input=%q action=%s chat_id=%d reply_to=%d text=%q\n", message.Text, action.Kind, action.ChatID, action.ReplyToMessageID, action.Text)
	fmt.Printf("ASSERT: action timestamp=%s is within dispatch window [%s, %s]\n", stamp.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))

	closeCtx, cancelClose := context.WithTimeout(rootCtx, 3*time.Second)
	if err := controller.Close(closeCtx); err != nil && !errors.Is(err, context.Canceled) {
		cancelClose()
		return fmt.Errorf("shutdown: %w", err)
	}
	cancelClose()
	fmt.Println("SHUTDOWN: controller stopped plugin process and removed its runtime socket")
	fmt.Println("TIME PLUGIN E2E PASS: .time -> parser -> registry -> supervisor RPC -> current-time reply action")
	return nil
}

// runPluginCommand calls the exact command engine behind `serenox plugin`
// while the heavyweight top-level binary (and therefore gotd) remains outside
// this deliberately narrow E2E boundary.
func runPluginCommand(ctx context.Context, arguments ...string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := plugincommands.Run(ctx, arguments, &stdout, &stderr); err != nil {
		return "", fmt.Errorf("serenox plugin %s: %w; stderr=%q", strings.Join(arguments, " "), err, stderr.String())
	}
	if stderr.Len() != 0 {
		return "", fmt.Errorf("serenox plugin %s wrote unexpected stderr %q", strings.Join(arguments, " "), stderr.String())
	}
	return stdout.String(), nil
}
