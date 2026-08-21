// Package plugincommands implements the user-facing lifecycle commands for
// deterministic Serenox .srnx plugin packages.
package plugincommands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
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
  pack       build a deterministic .srnx archive
  verify     validate content, hashes, signature, platform, and versions
  install    verify, stage, atomically install, and activate a package
  list       list installed plugins and retained versions
  rollback   activate an earlier installed version
  uninstall  remove one version or the entire plugin

Run %s <command> -h for command options.
`, program, program)
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
	case "verify":
		return runVerify(ctx, arguments[1:], stdout, stderr)
	case "install":
		return runInstall(ctx, arguments[1:], stdout, stderr)
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

func runPack(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("pack", stderr)
	source := flags.String("source", ".", "payload source directory")
	manifest := flags.String("manifest", "", "manifest template (default: <source>/manifest.json)")
	output := flags.String("output", "", "output .srnx filename (required)")
	keyFilename := flags.String("signing-key", "", "Ed25519 seed/private key or PKCS#8 PEM")
	keyID := flags.String("key-id", "", "signer key id (default: public key fingerprint)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("pack accepts options only")
	}
	if *output == "" {
		return fmt.Errorf("pack requires --output")
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
		return err
	}
	absolute, _ := filepath.Abs(*output)
	fmt.Fprintf(stdout, "packed %s@%s %s -> %s\n", verified.Manifest.ID, verified.Manifest.Version, verified.ManifestDigest, absolute)
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
	fmt.Fprintf(stdout, "verified %s@%s %s %s\n", verified.Manifest.ID, verified.Manifest.Version, verified.ManifestDigest, status)
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
	if flags.NArg() != 1 {
		return fmt.Errorf("install requires exactly one package filename")
	}
	options, err := verification.options()
	if err != nil {
		return err
	}
	// Installation always enforces platform and version compatibility.
	options.CheckPlatform = true
	options.CheckVersions = true
	manager, err := pluginpackage.NewManager(pluginpackage.ManagerConfig{Root: *root, Verify: options, RetainVersions: *retain})
	if err != nil {
		return err
	}
	receipt, err := manager.Install(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s@%s %s and activated it\n", receipt.ID, receipt.Version, receipt.ManifestDigest)
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
		fmt.Fprintln(stdout, "no plugins installed")
		return nil
	}
	for _, installed := range plugins {
		fmt.Fprintf(stdout, "%s (%s) active=%s\n", installed.ID, installed.Category, installed.ActiveVersion)
		for _, version := range installed.Versions {
			marker := " "
			if version.Active {
				marker = "*"
			}
			fmt.Fprintf(stdout, "  %s %s %s\n", marker, version.Receipt.Version, version.Receipt.ManifestDigest)
		}
	}
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
	fmt.Fprintf(stdout, "activated %s@%s %s\n", receipt.ID, receipt.Version, receipt.ManifestDigest)
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
		fmt.Fprintf(stdout, "uninstalled %s\n", flags.Arg(0))
	} else {
		fmt.Fprintf(stdout, "uninstalled %s@%s\n", flags.Arg(0), *version)
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

func defaultRoot() string {
	if configured := os.Getenv("SERENOX_DATA_DIR"); configured != "" {
		return configured
	}
	// Match config.Defaults().DataDir so the CLI and daemon reconcile the same
	// active-version records without extra flags in a local installation.
	return "data"
}
