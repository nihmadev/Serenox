// Package plugincommands implements the user-facing lifecycle commands for
// deterministic Serenox .srnx plugin packages.
package plugincommands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nihmadev/Serenox/internal/daemon"
	"github.com/nihmadev/Serenox/internal/migrationkit"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
	"github.com/nihmadev/Serenox/internal/pythonruntime"
)

const defaultProgram = "serenox plugin"

// Usage returns the command help for program. An empty program uses the
// canonical `serenox plugin` spelling.
func Usage(program string) string {
	if program == "" {
		program = defaultProgram
	}
	return fmt.Sprintf(`Usage: %s <command> [options]

Commands:
  init       create a migration-kit starter plugin
  preflight  validate a source tree and report migration hints
  build      compile the Go entrypoint and build a .srnx archive
  pack       package an already-built directory as a deterministic .srnx archive
  verify     validate content, hashes, signature, platform, and versions
  install    verify, stage, atomically install, and activate a package
  load       hot-load a .srnx package into an already running daemon
  list       list installed plugins and retained versions
  rollback   activate an earlier installed version
  uninstall  remove one version or the entire plugin

Quick start (run from the Serenox project root):
  cd ./my-plugin && %s install
  %s build --source ./my-plugin
  %s install ./dist/plugin.srnx

Build compiles the current platform entrypoint and writes ../dist/<id>-<version>.srnx.
Install accepts a .srnx file, a package directory, or no argument from inside a
plugin directory. When --root is omitted, the project data directory is used.

Run %s <command> -h for command options.
`, program, program, program, program, program)
}

// Run executes a plugin command using the canonical `serenox plugin` spelling
// in help output.
func Run(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	return RunAs(ctx, defaultProgram, arguments, stdout, stderr)
}

// RunAs executes a plugin command and uses program in help output. It is used
// by the legacy srnx-plugin and serenox-plugin compatibility binaries.
func RunAs(ctx context.Context, program string, arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		_, _ = io.WriteString(stderr, Usage(program))
		return fmt.Errorf("a command is required")
	}
	switch arguments[0] {
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, Usage(program))
		return nil
	case "pack":
		return runPack(ctx, arguments[1:], stdout, stderr)
	case "init", "scaffold":
		return runInit(arguments[1:], stdout, stderr)
	case "preflight":
		return runPreflight(ctx, arguments[1:], stdout, stderr)
	case "build":
		return runBuild(ctx, arguments[1:], stdout, stderr)
	case "verify":
		return runVerify(ctx, arguments[1:], stdout, stderr)
	case "install":
		return runInstall(ctx, arguments[1:], stdout, stderr)
	case "load", "hot-load":
		return runLoad(ctx, arguments[1:], stdout, stderr)
	case "list":
		return runList(ctx, arguments[1:], stdout, stderr)
	case "rollback":
		return runRollback(ctx, arguments[1:], stdout, stderr)
	case "uninstall":
		return runUninstall(ctx, arguments[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown plugin command %q", arguments[0])
	}
}

func runInit(arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("init", stderr)
	template := flags.String("template", "uwu", "migration starter template")
	output := flags.String("output", "./my-uwu", "new plugin directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("init accepts options only")
	}
	if err := migrationkit.Scaffold(*output, *template); err != nil {
		return err
	}
	absolute, _ := filepath.Abs(*output)
	writeOutputBlock(stdout, "Migration starter created", outputField{name: "Template", value: *template}, outputField{name: "Directory", value: absolute}, outputField{name: "Next", value: "serenox plugin preflight --source " + absolute})
	return nil
}

func runPreflight(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("preflight", stderr)
	source := flags.String("source", ".", "plugin source directory")
	manifest := flags.String("manifest", "", "manifest path relative to source (default: manifest.json)")
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("preflight accepts options only")
	}
	report, err := migrationkit.Preflight(ctx, *source, *manifest)
	if err != nil {
		return err
	}
	if *asJSON {
		if writeErr := writeJSON(stdout, report); writeErr != nil {
			return writeErr
		}
	} else {
		status := "PASS"
		if report.HasErrors() {
			status = "FAIL"
		}
		writeOutputBlock(stdout, "Migration preflight "+status, outputField{name: "Source", value: report.Source}, outputField{name: "Package", value: map[bool]string{true: "valid", false: "invalid"}[report.PackageValid]})
		if len(report.CapabilityNeeds) > 0 {
			fmt.Fprintln(stdout, "Capability hints:")
			for _, need := range report.CapabilityNeeds {
				if len(need.Permissions) == 0 {
					fmt.Fprintf(stdout, "  - %s\n", need.Token)
				} else {
					fmt.Fprintf(stdout, "  - %s (%s)\n", need.Token, strings.Join(need.Permissions, ", "))
				}
			}
			fmt.Fprintln(stdout)
		}
		if len(report.Findings) > 0 {
			fmt.Fprintln(stdout, "Migration findings:")
			for _, finding := range report.Findings {
				location := finding.File
				if finding.Line > 0 {
					location = fmt.Sprintf("%s:%d", location, finding.Line)
				}
				fmt.Fprintf(stdout, "  [%s] %s %s: %s\n", strings.ToUpper(string(finding.Severity)), finding.Code, location, finding.Message)
				if finding.Legacy != "" {
					fmt.Fprintf(stdout, "      %s -> %s\n", finding.Legacy, finding.Replacement)
				}
			}
			fmt.Fprintln(stdout)
		}
		fmt.Fprintln(stdout, "The source scan is conservative; review every warning before publishing.")
	}
	if report.HasErrors() {
		return fmt.Errorf("migration preflight failed; fix errors before packing")
	}
	return nil
}

func runLoad(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("load", stderr)
	root := flags.String("root", defaultRoot(), "Serenox daemon data root")
	socket := flags.String("socket", "", "daemon control socket (default: <root>/.serenox/control.sock)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("load requires exactly one .srnx package filename")
	}
	packagePath, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve plugin package: %w", err)
	}
	if _, err := os.Stat(packagePath); err != nil {
		return fmt.Errorf("inspect plugin package: %w", err)
	}
	dataRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve daemon data root: %w", err)
	}
	socketPath := *socket
	if socketPath == "" {
		if err := daemon.LoadPlugin(ctx, dataRoot, packagePath); err != nil {
			return err
		}
	} else {
		socketPath, err = filepath.Abs(socketPath)
		if err != nil {
			return fmt.Errorf("resolve daemon control socket: %w", err)
		}
		if err := daemon.LoadPluginAt(ctx, socketPath, daemon.ControlTokenPathForSocket(socketPath), packagePath); err != nil {
			return err
		}
	}
	writeOutputBlock(stdout, "Plugin hot-loaded", outputField{name: "Package", value: packagePath}, outputField{name: "Status", value: "running"})
	return nil
}

func runBuild(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("build", stderr)
	source := flags.String("source", ".", "Go plugin source directory")
	manifest := flags.String("manifest", "", "manifest template (default: <source>/manifest.json)")
	output := flags.String("output", "", "output .srnx filename (default: ../dist/<id>-<version>.srnx)")
	goCommand := flags.String("go", "go", "Go command used to compile the entrypoint")
	keyFilename := flags.String("signing-key", "", "Ed25519 seed/private key or PKCS#8 PEM")
	keyID := flags.String("key-id", "", "signer key id (default: public key fingerprint)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("build accepts options only")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sourceDirectory, err := filepath.Abs(*source)
	if err != nil {
		return fmt.Errorf("resolve source directory: %w", err)
	}
	identity, err := readManifestIdentity(sourceDirectory, *manifest)
	if err != nil {
		return fmt.Errorf("read manifest template: %w", err)
	}
	if identity.Category != pluginpackage.CategoryGoExecutable {
		return fmt.Errorf("build supports %q packages; use `serenox plugin pack` for %q", pluginpackage.CategoryGoExecutable, identity.Category)
	}
	entrypoint, err := selectBuildEntrypoint(identity.Entrypoints)
	if err != nil {
		return err
	}
	binary, err := installedEntrypointPath(sourceDirectory, entrypoint.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		return fmt.Errorf("create entrypoint directory: %w", err)
	}
	if err := buildEntrypoint(ctx, *goCommand, sourceDirectory, binary, stdout, stderr); err != nil {
		return fmt.Errorf("build entrypoint %q: %w", entrypoint.Path, err)
	}

	if *output == "" {
		inferred, inferErr := defaultPackageOutput(sourceDirectory, *manifest)
		if inferErr != nil {
			return fmt.Errorf("build requires --output when the package name cannot be inferred: %w", inferErr)
		}
		*output = inferred
	}
	options := pluginpackage.PackOptions{Limits: pluginpackage.DefaultLimits(), KeyID: *keyID}
	if *keyFilename != "" {
		privateKey, keyErr := pluginpackage.LoadPrivateKey(*keyFilename)
		if keyErr != nil {
			return keyErr
		}
		options.PrivateKey = privateKey
	}
	verified, err := pluginpackage.Pack(ctx, sourceDirectory, *manifest, *output, options)
	if err != nil {
		return packInputHint(sourceDirectory, *manifest, err)
	}
	absolute, _ := filepath.Abs(*output)
	writeOutputBlock(stdout, "Plugin built", outputField{name: "Plugin", value: verified.Manifest.ID}, outputField{name: "Version", value: verified.Manifest.Version}, outputField{name: "Entrypoint", value: entrypoint.Path}, outputField{name: "Binary", value: binary})
	writeOutputBlock(stdout, "Package packed", outputField{name: "Archive", value: absolute}, outputField{name: "Digest", value: verified.ManifestDigest})
	return nil
}

func runPack(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("pack", stderr)
	source := flags.String("source", ".", "payload source directory")
	manifest := flags.String("manifest", "", "manifest template (default: <source>/manifest.json)")
	output := flags.String("output", "", "output .srnx filename (default: ../dist/<id>-<version>.srnx)")
	keyFilename := flags.String("signing-key", "", "Ed25519 seed/private key or PKCS#8 PEM")
	keyID := flags.String("key-id", "", "signer key id (default: public key fingerprint)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("pack accepts options only")
	}
	if *output == "" {
		inferred, err := defaultPackageOutput(*source, *manifest)
		if err != nil {
			return fmt.Errorf("pack requires --output when the package name cannot be inferred: %w", err)
		}
		*output = inferred
	}
	options := pluginpackage.PackOptions{Limits: pluginpackage.DefaultLimits(), KeyID: *keyID}
	if *keyFilename != "" {
		privateKey, err := pluginpackage.LoadPrivateKey(*keyFilename)
		if err != nil {
			return err
		}
		options.PrivateKey = privateKey
	}
	verified, err := pluginpackage.Pack(ctx, *source, *manifest, *output, options)
	if err != nil {
		return packInputHint(*source, *manifest, err)
	}
	absolute, _ := filepath.Abs(*output)
	writeOutputBlock(stdout, "Package packed", outputField{name: "Plugin", value: verified.Manifest.ID}, outputField{name: "Version", value: verified.Manifest.Version}, outputField{name: "Digest", value: verified.ManifestDigest}, outputField{name: "Archive", value: absolute})
	return nil
}

func selectBuildEntrypoint(entrypoints []pluginpackage.Entrypoint) (pluginpackage.Entrypoint, error) {
	for _, entrypoint := range entrypoints {
		if entrypoint.OS == runtime.GOOS && entrypoint.Arch == runtime.GOARCH {
			return entrypoint, nil
		}
	}
	return pluginpackage.Entrypoint{}, fmt.Errorf("manifest has no Go entrypoint for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func installedEntrypointPath(sourceDirectory, relativePath string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(relativePath))
	if cleaned == "." || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("entrypoint path %q must be relative to the package directory", relativePath)
	}
	binary := filepath.Join(sourceDirectory, cleaned)
	relative, err := filepath.Rel(sourceDirectory, binary)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("entrypoint path %q escapes the package directory", relativePath)
	}
	return binary, nil
}

func buildEntrypoint(ctx context.Context, goCommand, sourceDirectory, binary string, stdout, stderr io.Writer) error {
	temporary, err := os.CreateTemp(filepath.Dir(binary), ".serenox-build-*")
	if err != nil {
		return fmt.Errorf("create temporary build output: %w", err)
	}
	temporaryName := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("close temporary build output: %w", err)
	}
	if err := os.Remove(temporaryName); err != nil {
		return fmt.Errorf("prepare temporary build output: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	command := exec.CommandContext(ctx, goCommand, "build", "-trimpath", "-buildvcs=false", "-o", temporaryName, ".")
	command.Dir = sourceDirectory
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, binary); err != nil {
		return fmt.Errorf("commit built entrypoint: %w", err)
	}
	committed = true
	return nil
}

type verifyFlags struct {
	trustFilename *string
	trustMode     *string
	targetOS      *string
	targetArch    *string
	coreVersion   *string
	protocol      *string
	allPlatforms  *bool
	skipVersions  *bool
}

func addVerifyFlags(flags *flag.FlagSet) verifyFlags {
	return verifyFlags{
		trustFilename: flags.String("trust", "", "JSON trust policy/key file"),
		trustMode:     flags.String("trust-mode", "", "allow-unsigned, require-signed, or require-trusted"),
		targetOS:      flags.String("os", runtime.GOOS, "target operating system"),
		targetArch:    flags.String("arch", runtime.GOARCH, "target architecture"),
		coreVersion:   flags.String("core-version", pluginpackage.DefaultCoreVersion, "running core version"),
		protocol:      flags.String("protocol-version", "1.0", "running plugin protocol version"),
		allPlatforms:  flags.Bool("all-platforms", false, "verify archive without selecting a platform entrypoint"),
		skipVersions:  flags.Bool("skip-version-check", false, "verify archive without checking core/protocol constraints"),
	}
}

func (values verifyFlags) options() (pluginpackage.VerifyOptions, error) {
	policy := pluginpackage.TrustPolicy{Mode: pluginpackage.TrustAllowUnsigned}
	if *values.trustFilename != "" {
		loaded, err := pluginpackage.LoadTrustPolicy(*values.trustFilename)
		if err != nil {
			return pluginpackage.VerifyOptions{}, err
		}
		policy = loaded
	}
	if *values.trustMode != "" {
		policy.Mode = pluginpackage.TrustMode(*values.trustMode)
	}
	return pluginpackage.VerifyOptions{
		Limits: pluginpackage.DefaultLimits(), Trust: policy,
		OS: *values.targetOS, Arch: *values.targetArch,
		CoreVersion: *values.coreVersion, ProtocolVersion: *values.protocol,
		CheckPlatform: !*values.allPlatforms, CheckVersions: !*values.skipVersions,
	}, nil
}

func runVerify(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("verify", stderr)
	verification := addVerifyFlags(flags)
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("verify requires exactly one package filename")
	}
	options, err := verification.options()
	if err != nil {
		return err
	}
	verified, err := pluginpackage.Verify(ctx, flags.Arg(0), options)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, verified)
	}
	status := "unsigned"
	if verified.Signed {
		status = "signed:" + verified.SignerKeyID
		if verified.Trusted {
			status += ":trusted"
		}
	}
	writeOutputBlock(stdout, "Package verified", outputField{name: "Plugin", value: verified.Manifest.ID}, outputField{name: "Version", value: verified.Manifest.Version}, outputField{name: "Digest", value: verified.ManifestDigest}, outputField{name: "Signature", value: status})
	return nil
}

func runInstall(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("install", stderr)
	root := flags.String("root", defaultRoot(), "Serenox plugin data root")
	retain := flags.Int("retain", pluginpackage.DefaultRetain, "number of installed versions to retain")
	verification := addVerifyFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New(installInputHint(""))
	}
	options, err := verification.options()
	if err != nil {
		return err
	}
	// Installation always enforces platform and version compatibility.
	options.CheckPlatform = true
	options.CheckVersions = true
	packageFilename := "."
	if flags.NArg() == 1 {
		packageFilename = flags.Arg(0)
	} else if _, statErr := os.Stat(filepath.Join(packageFilename, pluginpackage.ManifestName)); statErr != nil {
		return errors.New(installInputHint(""))
	}
	temporaryPackageDir := ""
	if info, statErr := os.Stat(packageFilename); statErr == nil {
		if info.IsDir() {
			if options.Trust.Mode != pluginpackage.TrustAllowUnsigned {
				return fmt.Errorf("cannot install source directory with trust mode %q: auto-packed source is unsigned; run `serenox plugin pack --signing-key ...` first, then install the resulting .srnx", options.Trust.Mode)
			}
			identity, readErr := readManifestIdentity(packageFilename, "")
			if readErr != nil {
				return packInputHint(packageFilename, "", fmt.Errorf("read package directory: %w", readErr))
			}
			if identity.Category == pluginpackage.CategoryGoExecutable {
				if err := runBuild(ctx, []string{"--source", packageFilename}, stdout, stderr); err != nil {
					return err
				}
				packageFilename, err = defaultPackageOutput(packageFilename, "")
				if err != nil {
					return err
				}
			} else {
				temporaryPackageDir, err = os.MkdirTemp("", "serenox-plugin-install-")
				if err != nil {
					return fmt.Errorf("create temporary package directory: %w", err)
				}
				defer func() { _ = os.RemoveAll(temporaryPackageDir) }()
				temporaryPackage := filepath.Join(temporaryPackageDir, "plugin.srnx")
				if err := runPack(ctx, []string{"--source", packageFilename, "--output", temporaryPackage}, stdout, stderr); err != nil {
					return err
				}
				packageFilename = temporaryPackage
			}
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		return errors.New(installInputHint(packageFilename))
	} else {
		return fmt.Errorf("inspect install input %q: %w", packageFilename, statErr)
	}
	pythonRoots := []string{"."}
	if executable, executableErr := os.Executable(); executableErr == nil {
		pythonRoots = append(pythonRoots, filepath.Join(filepath.Dir(executable), ".."))
	}
	pythonInfo, pythonErr := pythonruntime.Discover(ctx, pythonruntime.Policy{}, pythonRoots)
	manager, err := pluginpackage.NewManager(pluginpackage.ManagerConfig{
		Root: *root, Verify: options, RetainVersions: *retain,
		RuntimeValidator: func(runtimeContext context.Context, manifest pluginpackage.Manifest) error {
			if manifest.Category != pluginpackage.CategoryPythonSidecar {
				return nil
			}
			if pythonErr != nil {
				return pythonErr
			}
			return pythonruntime.ValidateManifest(pythonInfo, manifest)
		},
	})
	if err != nil {
		return err
	}
	receipt, err := manager.Install(ctx, packageFilename)
	if err != nil {
		return err
	}
	rootAbsolute, _ := filepath.Abs(*root)
	writeOutputBlock(stdout, "Plugin installed", outputField{name: "Plugin", value: receipt.ID}, outputField{name: "Version", value: receipt.Version}, outputField{name: "Digest", value: receipt.ManifestDigest}, outputField{name: "Status", value: "active"}, outputField{name: "Root", value: rootAbsolute})
	// Installation is also the developer-friendly activation path. The daemon
	// performs the second Install idempotently, then validates metadata and
	// starts the supervised process without restarting the core.
	if hotLoadErr := daemon.LoadPlugin(ctx, rootAbsolute, packageFilename); hotLoadErr != nil {
		if errors.Is(hotLoadErr, daemon.ErrControlUnavailable) {
			writeOutputBlock(stdout, "Daemon not running", outputField{name: "Status", value: "installed; will load on next startup"})
			return nil
		}
		return fmt.Errorf("plugin installed but hot-load failed: %w", hotLoadErr)
	}
	writeOutputBlock(stdout, "Plugin hot-loaded", outputField{name: "Status", value: "running"})
	return nil
}

func runList(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("list", stderr)
	root := flags.String("root", defaultRoot(), "Serenox plugin data root")
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("list accepts options only")
	}
	manager, err := pluginpackage.NewManager(pluginpackage.ManagerConfig{Root: *root})
	if err != nil {
		return err
	}
	plugins, err := manager.List(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, plugins)
	}
	if len(plugins) == 0 {
		fmt.Fprintln(stdout, "No plugins installed.")
		return nil
	}
	fmt.Fprintf(stdout, "Installed plugins (%d)\n\n", len(plugins))
	for index, installed := range plugins {
		if index > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "Plugin: %s\n", installed.ID)
		fmt.Fprintf(stdout, "  %-10s %s\n", "Category:", installed.Category)
		active := installed.ActiveVersion
		if active == "" {
			active = "none"
		}
		fmt.Fprintf(stdout, "  %-10s %s\n", "Active:", active)
		fmt.Fprintln(stdout, "  Versions:")
		for _, version := range installed.Versions {
			marker := "-"
			if version.Active {
				marker = "*"
			}
			fmt.Fprintf(stdout, "    %s %s\n", marker, version.Receipt.Version)
			fmt.Fprintf(stdout, "      %-10s %s\n", "Digest:", version.Receipt.ManifestDigest)
		}
	}
	fmt.Fprintln(stdout)
	return nil
}

func runRollback(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("rollback", stderr)
	root := flags.String("root", defaultRoot(), "Serenox plugin data root")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		return fmt.Errorf("rollback requires a plugin id and optional target version")
	}
	target := ""
	if flags.NArg() == 2 {
		target = flags.Arg(1)
	}
	manager, err := pluginpackage.NewManager(pluginpackage.ManagerConfig{Root: *root})
	if err != nil {
		return err
	}
	receipt, err := manager.Rollback(ctx, flags.Arg(0), target)
	if err != nil {
		return err
	}
	writeOutputBlock(stdout, "Plugin activated", outputField{name: "Plugin", value: receipt.ID}, outputField{name: "Version", value: receipt.Version}, outputField{name: "Digest", value: receipt.ManifestDigest}, outputField{name: "Status", value: "active"})
	return nil
}

func runUninstall(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("uninstall", stderr)
	root := flags.String("root", defaultRoot(), "Serenox plugin data root")
	version := flags.String("version", "", "remove only this installed version")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("uninstall requires exactly one plugin id")
	}
	manager, err := pluginpackage.NewManager(pluginpackage.ManagerConfig{Root: *root})
	if err != nil {
		return err
	}
	if err := manager.Uninstall(ctx, flags.Arg(0), *version); err != nil {
		return err
	}
	if *version == "" {
		writeOutputBlock(stdout, "Plugin uninstalled", outputField{name: "Plugin", value: flags.Arg(0)}, outputField{name: "Versions", value: "all"})
	} else {
		writeOutputBlock(stdout, "Plugin version uninstalled", outputField{name: "Plugin", value: flags.Arg(0)}, outputField{name: "Version", value: *version})
	}
	return nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type outputField struct {
	name  string
	value any
}

func writeOutputBlock(output io.Writer, title string, fields ...outputField) {
	fmt.Fprintf(output, "%s\n\n", title)
	for _, field := range fields {
		fmt.Fprintf(output, "  %-10s %v\n", field.name+":", field.value)
	}
	fmt.Fprintln(output)
}

func defaultRoot() string {
	if configured := os.Getenv("SERENOX_DATA_DIR"); configured != "" {
		return configured
	}
	if discovered, ok := discoverProjectDataRoot(); ok {
		return discovered
	}
	// Match config.Defaults().DataDir when the CLI is used outside a project
	// tree. Inside a project, discoverProjectDataRoot keeps plugin commands
	// consistent even when invoked from a plugin's own directory.
	return "data"
}

func discoverProjectDataRoot() (string, bool) {
	directory, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		configFilename := filepath.Join(directory, "config.json")
		if data, readErr := os.ReadFile(configFilename); readErr == nil {
			var config struct {
				DataDir string `json:"data_dir"`
			}
			if json.Unmarshal(data, &config) == nil && strings.TrimSpace(config.DataDir) != "" {
				if filepath.IsAbs(config.DataDir) {
					return filepath.Clean(config.DataDir), true
				}
				return filepath.Join(directory, config.DataDir), true
			}
		}
		if _, statErr := os.Stat(filepath.Join(directory, ".git")); statErr == nil {
			return filepath.Join(directory, "data"), true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", false
		}
		directory = parent
	}
}

type manifestIdentity struct {
	ID          string                     `json:"id"`
	Version     string                     `json:"version"`
	Category    pluginpackage.Category     `json:"category"`
	Entrypoints []pluginpackage.Entrypoint `json:"entrypoints"`
}

func defaultPackageOutput(source, manifestFilename string) (string, error) {
	sourceDirectory, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve source directory: %w", err)
	}
	identity, err := readManifestIdentity(sourceDirectory, manifestFilename)
	if err != nil {
		return "", fmt.Errorf("read manifest template: %w", err)
	}
	if strings.TrimSpace(identity.ID) == "" || strings.TrimSpace(identity.Version) == "" {
		return "", errors.New("manifest must contain id and version")
	}
	return filepath.Join(filepath.Dir(sourceDirectory), "dist", identity.ID+"-"+identity.Version+".srnx"), nil
}

func installInputHint(input string) string {
	var builder strings.Builder
	if input == "" {
		builder.WriteString("install needs a .srnx package file or a package directory; from a plugin directory, run `serenox plugin install`")
	} else if input == "pack" {
		builder.WriteString("install expects a package path or package directory; `pack` is a command, not a package")
	} else {
		fmt.Fprintf(&builder, "plugin package or package directory %q was not found", input)
	}
	builder.WriteString("\n\nExamples:\n  cd ./my-plugin && serenox plugin install\n  serenox plugin install ./my-plugin\n  serenox plugin install ./dist/<plugin-id>-<version>.srnx\n\nThe install command builds Go source automatically and discovers the project\ndata directory. Use --root only when you need a custom installation root.")
	return builder.String()
}

func packInputHint(source, manifestFilename string, cause error) error {
	identity, err := readManifestIdentity(source, manifestFilename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w\n\nA package directory must contain %s. For a Go plugin, run `serenox plugin build --source <directory>` from the project root.", cause, pluginpackage.ManifestName)
		}
		return cause
	}
	if !strings.Contains(cause.Error(), "entrypoints") || !strings.Contains(cause.Error(), "not declared in files") || len(identity.Entrypoints) == 0 {
		return cause
	}
	entrypoint := filepath.FromSlash(identity.Entrypoints[0].Path)
	return fmt.Errorf("%w\n\nThe manifest expects a built entrypoint at %q, but that file is not in the package source. Run the build command first:\n  serenox plugin build", cause, entrypoint)
}

func readManifestIdentity(source, manifestFilename string) (manifestIdentity, error) {
	sourceDirectory, err := filepath.Abs(source)
	if err != nil {
		return manifestIdentity{}, err
	}
	if manifestFilename == "" {
		manifestFilename = filepath.Join(sourceDirectory, pluginpackage.ManifestName)
	}
	manifestData, err := os.ReadFile(manifestFilename)
	if err != nil {
		return manifestIdentity{}, err
	}
	var identity manifestIdentity
	if err := json.Unmarshal(manifestData, &identity); err != nil {
		return manifestIdentity{}, err
	}
	return identity, nil
}
