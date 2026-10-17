package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nihmadev/Serenox/internal/manifest"
	"github.com/nihmadev/Serenox/internal/release"
)

// ManifestHandler implements the declarative control-plane commands without
// starting Telegram or creating a second daemon lifecycle.
func ManifestHandler() CommandHandler {
	return func(ctx context.Context, arguments []string, _ io.Reader, stdout, stderr io.Writer) error {
		return executeControlPlane(ctx, arguments, stdout, stderr)
	}
}

func executeControlPlane(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("manifest, service, release, config effective/migrate, or backup subcommand is required")
	}
	group := arguments[0]
	args := arguments[1:]
	switch group {
	case "manifest":
		return executeManifest(ctx, args, stdout)
	case "service":
		return executeService(ctx, args, stdout)
	case "release":
		return executeRelease(ctx, args, stdout)
	case "backup":
		return executeBackup(ctx, args, stdout)
	case "config":
		return executeConfig(ctx, args, stdout)
	default:
		return fmt.Errorf("unknown control-plane command %q", group)
	}
}

func executeManifest(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("manifest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "serenox.yaml", "manifest path")
	format := flags.String("format", "", "render format: systemd or compose")
	output := flags.String("output", "", "output file")
	includeRefs := flags.Bool("include-secret-references", false, "include reference names in generated comments")
	if len(args) == 0 {
		return errors.New("manifest requires check or render")
	}
	command := args[0]
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	effective, err := manifest.LoadEffective(manifest.LoadOptions{Path: *file})
	if err != nil {
		return err
	}
	switch command {
	case "check":
		fmt.Fprintf(stdout, "manifest OK: %s\ndigest: %s\n", *file, effective.Digest)
		return nil
	case "render":
		var data string
		switch *format {
		case "systemd":
			data, err = manifest.RenderSystemd(effective, manifest.RenderOptions{IncludeReferences: *includeRefs})
		case "compose":
			data, err = manifest.RenderCompose(effective, manifest.RenderOptions{IncludeReferences: *includeRefs})
		default:
			return errors.New("manifest render requires --format systemd or compose")
		}
		if err != nil {
			return err
		}
		return writeOrPrint(*output, data, stdout)
	default:
		return fmt.Errorf("unknown manifest command %q", command)
	}
}

func executeService(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("service", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("manifest", "serenox.yaml", "manifest path")
	if len(args) == 0 || args[0] != "check" {
		return errors.New("service requires the check subcommand")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	effective, err := manifest.LoadEffective(manifest.LoadOptions{Path: *file})
	if err != nil {
		return err
	}
	registry := manifest.NewRegistry()
	registry.SetProbe("postgres", manifest.TCPProbe{})
	registry.SetProbe("redis", manifest.TCPProbe{})
	registry.SetProbe("object_storage", manifest.TCPProbe{})
	statuses, checkErr := registry.CheckDeclared(ctx, effective.Document)
	for _, status := range statuses {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", status.Name, status.State, status.Detail)
	}
	return checkErr
}

func executeRelease(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("release requires plan, apply, status, or rollback")
	}
	command := args[0]
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("manifest", "serenox.yaml", "manifest path")
	yes := flags.Bool("yes", false, "approve an apply")
	releaseID := flags.String("release", "", "release ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if command == "status" {
		effective, err := manifest.LoadEffective(manifest.LoadOptions{Path: *file})
		if err != nil {
			return err
		}
		records, active, err := release.NewManager().Status(effective.Runtime.DataDir)
		if err != nil {
			return err
		}
		return printJSON(stdout, map[string]any{"active": active, "releases": records})
	}
	effective, err := manifest.LoadEffective(manifest.LoadOptions{Path: *file})
	if err != nil {
		return err
	}
	manager := release.NewManager()
	switch command {
	case "plan":
		plan, err := manager.Plan(ctx, effective)
		if err != nil {
			return err
		}
		return printJSON(stdout, plan)
	case "apply":
		record, err := manager.Apply(ctx, effective, *yes)
		if err != nil {
			return err
		}
		return printJSON(stdout, record)
	case "rollback":
		record, err := manager.Rollback(effective.Runtime.DataDir, *releaseID)
		if err != nil {
			return err
		}
		return printJSON(stdout, record)
	default:
		return fmt.Errorf("unknown release command %q", command)
	}
}

func executeBackup(ctx context.Context, args []string, stdout io.Writer) error {
	_ = ctx
	if len(args) == 0 {
		return errors.New("backup requires create, verify, or restore")
	}
	command := args[0]
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("manifest", "serenox.yaml", "manifest path")
	input := flags.String("input", "", "backup path")
	output := flags.String("output", "", "backup output path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch command {
	case "verify":
		if *input == "" {
			return errors.New("backup verify requires --input")
		}
		value, err := manifest.VerifyBackup(*input, 64<<20)
		if err != nil {
			return err
		}
		return printJSON(stdout, value)
	}
	effective, err := manifest.LoadEffective(manifest.LoadOptions{Path: *file})
	if err != nil {
		return err
	}
	switch command {
	case "create":
		path, err := manifest.CreateBackup(effective, manifest.BackupOptions{OutputPath: *output})
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, path)
		return nil
	case "restore":
		if *input == "" {
			return errors.New("backup restore requires --input")
		}
		return manifest.RestoreBackup(effective, *input)
	default:
		return fmt.Errorf("unknown backup command %q", command)
	}
}

func executeConfig(ctx context.Context, args []string, stdout io.Writer) error {
	_ = ctx
	if len(args) == 0 {
		return errors.New("config requires effective or migrate")
	}
	command := args[0]
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	manifestPath := flags.String("manifest", "serenox.yaml", "manifest path")
	from := flags.String("from", "", "legacy JSON path")
	to := flags.String("to", "", "new manifest path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch command {
	case "effective":
		effective, err := manifest.LoadEffective(manifest.LoadOptions{Path: *manifestPath})
		if err != nil {
			return err
		}
		data, err := effective.RedactedJSON()
		if err != nil {
			return err
		}
		_, err = stdout.Write(data)
		return err
	case "migrate":
		report, err := manifest.MigrateLegacy(*from, *to)
		if err != nil {
			return err
		}
		return printJSON(stdout, report)
	default:
		return fmt.Errorf("unknown config command %q", command)
	}
}

func writeOrPrint(path, data string, stdout io.Writer) error {
	if path == "" {
		_, err := io.WriteString(stdout, data)
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(data), 0o600)
}

func printJSON(stdout io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, string(data)+"\n")
	return err
}
