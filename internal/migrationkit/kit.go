// Package migrationkit contains the user-facing migration helpers. It is
// intentionally conservative: the preflight report is a review aid, not a
// promise that arbitrary Python can be made compatible by heuristics.
package migrationkit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type Finding struct {
	Severity    Severity `json:"severity"`
	Code        string   `json:"code"`
	File        string   `json:"file,omitempty"`
	Line        int      `json:"line,omitempty"`
	Legacy      string   `json:"legacy,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
	Message     string   `json:"message"`
}

type CapabilityNeed struct {
	Token       string   `json:"token"`
	Evidence    string   `json:"evidence"`
	Permissions []string `json:"permissions,omitempty"`
}

type Report struct {
	Source          string                         `json:"source"`
	Manifest        string                         `json:"manifest"`
	PackageValid    bool                           `json:"package_valid"`
	PackageError    string                         `json:"package_error,omitempty"`
	Package         *pluginpackage.VerifiedPackage `json:"package,omitempty"`
	Findings        []Finding                      `json:"findings"`
	CapabilityNeeds []CapabilityNeed               `json:"capability_needs"`
}

type rule struct {
	code        string
	severity    Severity
	legacy      string
	replacement string
	message     string
	pattern     *regexp.Regexp
}

var rules = []rule{
	{code: "raw-client", severity: SeverityError, legacy: "Pyrogram/Telethon/Aiogram client", replacement: "Message DTOs, ClientProxy, and explicit host actions", message: "Raw Telegram library objects cannot cross the Serenox boundary.", pattern: regexp.MustCompile(`(?m)^[ \t]*(?:from|import)[ \t]+(?:pyrogram|telethon|aiogram)(?:\.|[ \t]|$)`)},
	{code: "runtime-install", severity: SeverityError, legacy: "pip/import_lib", replacement: "Pack dependencies into the verified .srnx artifact", message: "Runtime code installation is intentionally unsupported.", pattern: regexp.MustCompile(`(?i)\b(?:pip|import_lib|install_lib|requirements\.txt)\b`)},
	{code: "shell", severity: SeverityError, legacy: "eval/terminal/shell", replacement: "typed host services or a bounded pure-Python operation", message: "Shell and arbitrary evaluation are not part of the plugin contract.", pattern: regexp.MustCompile(`(?m)\b(?:os\.system|subprocess\.|\.run\(|\.Popen\(|eval\(|exec\()`)},
	{code: "legacy-loop", severity: SeverityWarning, legacy: "@loader.loop", replacement: "a bounded event handler; scheduling needs an explicit future API", message: "Unbounded background loops are not portable across restart and overload boundaries.", pattern: regexp.MustCompile(`(?m)@[^\n]*loader\.loop`)},
	{code: "legacy-db", severity: SeverityWarning, legacy: "self.db.get/set or pointer storage", replacement: "await self.async_get/set/delete with a small JSON value", message: "Use the namespaced, bounded storage contract and handle overload explicitly.", pattern: regexp.MustCompile(`(?m)\b(?:self\._?db|self\.pointer|self\.get\(|self\.set\()`)},
	{code: "legacy-inline", severity: SeverityWarning, legacy: "self.inline/raw callback state", replacement: "bounded inline/callback DTOs and opaque callback IDs", message: "Inline state is bounded and does not survive a sidecar restart.", pattern: regexp.MustCompile(`(?m)\bself\.inline\b`)},
	{code: "raw-client-method", severity: SeverityWarning, legacy: "client.<arbitrary method>", replacement: "the documented ClientProxy method or a new typed protocol operation", message: "Only explicitly supported client operations are available.", pattern: regexp.MustCompile(`(?m)\b(?:client|self\.client)\.(?:invoke|raw|download_media|get_chat|iter_|send_file|forward_messages)\b`)},
}

var capabilityRules = []struct {
	token       string
	permissions []string
	pattern     *regexp.Regexp
	evidence    string
}{
	{token: "commands", permissions: []string{"telegram.reply"}, pattern: regexp.MustCompile(`(?m)@[^\n]*\b(?:command|owner)\b|\b\w+cmd\s*\(`), evidence: "command decorator or *cmd method"},
	{token: "watchers", permissions: []string{"telegram.reply"}, pattern: regexp.MustCompile(`(?m)@[^\n]*\bwatcher\b|\bwatcher\s*\(`), evidence: "watcher decorator or watcher method"},
	{token: "pre_send", permissions: nil, pattern: regexp.MustCompile(`(?m)@[^\n]*\bpre_send\b|\bpre_send\s*\(`), evidence: "pre-send transformation hook"},
	{token: "inline", permissions: []string{"telegram.inline"}, pattern: regexp.MustCompile(`(?m)@[^\n]*\b(?:inline_handler|callback_handler)\b|\bself\.inline\b`), evidence: "inline/callback handler or inline proxy"},
	{token: "storage", permissions: []string{"storage.read", "storage.write"}, pattern: regexp.MustCompile(`(?m)\b(?:async_get|async_set|async_delete|self\._?db|self\.get\(|self\.set\()`), evidence: "namespaced storage access"},
	{token: "http", permissions: []string{"network.http.read"}, pattern: regexp.MustCompile(`(?m)\b(?:self\.http|HTTPClient|requests\.|aiohttp\.|httpx\.)`), evidence: "HTTP client usage"},
	{token: "media_read", permissions: []string{"media.read"}, pattern: regexp.MustCompile(`(?m)\b(?:download_media|MediaClient|\.media\b)`), evidence: "media read or transfer usage"},
	{token: "media_attach", permissions: []string{"media.attach"}, pattern: regexp.MustCompile(`(?m)\b(?:send_file|media\.attach|MediaAttachment)`), evidence: "media attachment usage"},
}

// Preflight validates a source directory through the same deterministic
// packer used by `serenox plugin pack`, then adds conservative source hints.
// It never writes into the source directory.
func Preflight(ctx context.Context, source, manifest string) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return Report{}, fmt.Errorf("resolve source: %w", err)
	}
	if manifest == "" {
		manifest = filepath.Join(absolute, pluginpackage.ManifestName)
	} else if !filepath.IsAbs(manifest) {
		manifest = filepath.Join(absolute, manifest)
	}
	report := Report{Source: absolute, Manifest: manifest, Findings: []Finding{}}

	temporary, err := os.CreateTemp("", "serenox-preflight-*.srnx")
	if err != nil {
		return Report{}, fmt.Errorf("create preflight archive: %w", err)
	}
	temporaryName := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		_ = os.Remove(temporaryName)
		return Report{}, fmt.Errorf("close preflight archive: %w", closeErr)
	}
	defer os.Remove(temporaryName)

	verified, packErr := pluginpackage.Pack(ctx, absolute, manifest, temporaryName, pluginpackage.PackOptions{})
	if packErr != nil {
		report.PackageError = packErr.Error()
		report.Findings = append(report.Findings, Finding{
			Severity: SeverityError, Code: "manifest-or-package", File: filepath.Base(manifest),
			Message: "The source cannot be packed by the canonical Serenox verifier: " + packErr.Error(),
		})
	} else {
		report.PackageValid = true
		report.Package = &verified
	}

	scanErr := scanPython(ctx, absolute, &report)
	if scanErr != nil {
		return report, scanErr
	}
	report.CapabilityNeeds = inferCapabilities(report.Findings, absolute)
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].File != report.Findings[j].File {
			return report.Findings[i].File < report.Findings[j].File
		}
		if report.Findings[i].Line != report.Findings[j].Line {
			return report.Findings[i].Line < report.Findings[j].Line
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})
	return report, nil
}

func scanPython(ctx context.Context, source string, report *Report) error {
	return filepath.WalkDir(source, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == ".venv" || name == "__pycache__" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(filename) != ".py" {
			return nil
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		text := string(data)
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		for _, current := range rules {
			locations := current.pattern.FindAllStringIndex(text, -1)
			for _, location := range locations {
				line := 1 + strings.Count(text[:location[0]], "\n")
				report.Findings = append(report.Findings, Finding{
					Severity: current.severity, Code: current.code, File: filepath.ToSlash(relative), Line: line,
					Legacy: current.legacy, Replacement: current.replacement, Message: current.message,
				})
			}
		}
		return nil
	})
}

func inferCapabilities(findings []Finding, source string) []CapabilityNeed {
	data, err := readPythonSource(source)
	if err != nil {
		return nil
	}
	result := make([]CapabilityNeed, 0, len(capabilityRules))
	for _, rule := range capabilityRules {
		if !rule.pattern.MatchString(data) {
			continue
		}
		result = append(result, CapabilityNeed{Token: rule.token, Evidence: rule.evidence, Permissions: append([]string(nil), rule.permissions...)})
	}
	_ = findings
	sort.Slice(result, func(i, j int) bool { return result[i].Token < result[j].Token })
	return result
}

func readPythonSource(source string) (string, error) {
	var builder strings.Builder
	err := filepath.WalkDir(source, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".venv" || entry.Name() == "__pycache__" || entry.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(filename) != ".py" {
			return nil
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		builder.Write(data)
		builder.WriteByte('\n')
		return nil
	})
	return builder.String(), err
}

func (r Report) HasErrors() bool {
	if !r.PackageValid {
		return true
	}
	for _, finding := range r.Findings {
		if finding.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
