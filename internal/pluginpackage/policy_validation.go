package pluginpackage

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

func validateResourceLimits(limits ResourceLimits) error {
	checks := []struct {
		name  string
		value int
		min   int
		max   int
	}{
		{"queue_capacity", limits.QueueCapacity, 0, 65536},
		{"workers", limits.Workers, 0, 32},
		{"action_batch", limits.ActionBatch, 0, 128},
		{"action_bytes", limits.ActionBytes, 0, 1 << 20},
		{"storage_entries", limits.StorageEntries, 0, 1_000_000},
		{"storage_value_bytes", limits.StorageValueBytes, 0, 16 << 20},
		{"callback_capacity", limits.CallbackCapacity, 0, 65536},
		{"max_frame_bytes", limits.MaxFrameBytes, 0, 64 << 20},
		{"handler_timeout_ms", limits.HandlerTimeoutMS, 0, 300_000},
		{"shutdown_timeout_ms", limits.ShutdownTimeoutMS, 0, 60_000},
		{"max_jobs", limits.MaxJobs, 0, 256},
		{"max_job_payload_bytes", limits.MaxJobPayloadBytes, 0, 64 << 10},
		{"max_job_runtime_ms", limits.MaxJobRuntimeMS, 0, 86_400_000},
		{"media_chunk_size_bytes", limits.MediaChunkSizeBytes, 0, 64 << 10},
		{"media_max_concurrent", limits.MediaMaxConcurrent, 0, 64},
		{"media_transfer_timeout_ms", limits.MediaTransferTimeoutMS, 0, 300_000},
		{"media_idle_timeout_ms", limits.MediaIdleTimeoutMS, 0, 30_000},
	}
	for _, check := range checks {
		if check.value < check.min || check.value > check.max {
			return invalid("resources."+check.name, "must be between %d and %d", check.min, check.max)
		}
	}
	if limits.ActionBytes > 0 && limits.MaxFrameBytes > 0 && limits.ActionBytes > limits.MaxFrameBytes {
		return invalid("resources.action_bytes", "cannot exceed resources.max_frame_bytes")
	}
	if limits.MediaMaxSizeBytes < 0 || limits.MediaMaxSizeBytes > 64<<20 {
		return invalid("resources.media_max_size_bytes", "must be between 0 and %d", 64<<20)
	}
	if limits.MediaQuotaBytes < 0 || limits.MediaQuotaBytes > 256<<20 {
		return invalid("resources.media_quota_bytes", "must be between 0 and %d", 256<<20)
	}
	if limits.MediaMaxSizeBytes > 0 && limits.MediaQuotaBytes > 0 && limits.MediaQuotaBytes < limits.MediaMaxSizeBytes {
		return invalid("resources.media_quota_bytes", "cannot be smaller than media_max_size_bytes")
	}
	return nil
}

func validateNetworkPolicy(policy NetworkPolicy) error {
	if len(policy.Origins) > 64 {
		return invalid("network.origins", "must contain at most 64 origins")
	}
	if policy.MaxRequestBytes < 0 || policy.MaxRequestBytes > 512<<10 || policy.MaxResponseBytes < 0 || policy.MaxResponseBytes > 512<<10 {
		return invalid("network", "request/response bytes are outside bounded limits")
	}
	if policy.MaxRedirects < 0 || policy.MaxRedirects > 10 || policy.RequestTimeoutMS < 0 || policy.RequestTimeoutMS > 300000 || policy.ConnectionTimeoutMS < 0 || policy.ConnectionTimeoutMS > 300000 || policy.HeaderTimeoutMS < 0 || policy.HeaderTimeoutMS > 300000 || policy.IdleTimeoutMS < 0 || policy.IdleTimeoutMS > 300000 {
		return invalid("network", "timeout or redirect limit is outside bounded limits")
	}
	if policy.MaxConcurrent < 0 || policy.MaxConcurrent > 256 || policy.RequestsPerSecond < 0 || policy.RequestsPerSecond > 100 || policy.Burst < 0 || policy.Burst > 256 || policy.TotalByteBudget < 0 || policy.TotalByteBudget > 1<<34 {
		return invalid("network", "concurrency, rate, or byte budget is outside bounded limits")
	}
	seen := make(map[string]struct{}, len(policy.Origins))
	for i, origin := range policy.Origins {
		if origin.Scheme != "https" && origin.Scheme != "http" {
			return invalid(fmt.Sprintf("network.origins[%d].scheme", i), "must be http or https")
		}
		if origin.Scheme == "http" && !policy.AllowHTTP {
			return invalid(fmt.Sprintf("network.origins[%d]", i), "cleartext HTTP is disabled by policy")
		}
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(origin.Host)), ".")
		if host == "" || strings.ContainsAny(host, "/?#@") || net.ParseIP(host) != nil {
			return invalid(fmt.Sprintf("network.origins[%d].host", i), "must be a normalized DNS hostname")
		}
		if strings.Contains(host, "*") || !validHostname(host) {
			return invalid(fmt.Sprintf("network.origins[%d].host", i), "wildcards and invalid hostnames are not allowed")
		}
		port := origin.Port
		if port == 0 {
			port = 443
			if origin.Scheme == "http" {
				port = 80
			}
		}
		if port < 1 || port > 65535 {
			return invalid(fmt.Sprintf("network.origins[%d].port", i), "must be between 1 and 65535")
		}
		key := origin.Scheme + "://" + host + ":" + fmt.Sprint(port)
		if origin.Subdomains {
			key += ":subdomains"
		}
		if _, exists := seen[key]; exists {
			return invalid("network.origins", "duplicate origin %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validHostname(host string) bool {
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}

func validateDependencies(metadata DependencyMetadata) error {
	if len(metadata.Packages) > 256 {
		return invalid("dependencies.packages", "must contain at most 256 entries")
	}
	if metadata.LockSHA256 != "" && !hexDigestPattern.MatchString(metadata.LockSHA256) {
		return invalid("dependencies.lock_sha256", "must be 64 lowercase hexadecimal characters")
	}
	seen := make(map[string]struct{}, len(metadata.Packages))
	for index, dependency := range metadata.Packages {
		if !pythonDependencyPattern.MatchString(dependency.Name) {
			return invalid(fmt.Sprintf("dependencies.packages[%d].name", index), "must be a normalized package name")
		}
		if !versionPattern.MatchString(dependency.Version) {
			return invalid(fmt.Sprintf("dependencies.packages[%d].version", index), "must be a version constraint")
		}
		if _, exists := seen[dependency.Name]; exists {
			return invalid("dependencies.packages", "duplicate package %q", dependency.Name)
		}
		seen[dependency.Name] = struct{}{}
	}
	sort.Slice(metadata.Packages, func(i, j int) bool { return metadata.Packages[i].Name < metadata.Packages[j].Name })
	return nil
}
