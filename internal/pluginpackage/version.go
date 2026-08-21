package pluginpackage

import (
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion struct {
	major, minor, patch uint64
	pre                 []string
}

// parseVersion accepts semver-style versions with two or three numeric
// components. The two-component form exists because protocol v1 is named 1.0.
func parseVersion(raw string) (semanticVersion, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return semanticVersion{}, fmt.Errorf("invalid version %q", raw)
	}
	withoutBuild := strings.SplitN(raw, "+", 2)[0]
	parts := strings.SplitN(withoutBuild, "-", 2)
	numbers := strings.Split(parts[0], ".")
	if len(numbers) < 2 || len(numbers) > 3 {
		return semanticVersion{}, fmt.Errorf("version %q needs two or three numeric components", raw)
	}
	values := [3]uint64{}
	for i, number := range numbers {
		if number == "" || (len(number) > 1 && number[0] == '0') {
			return semanticVersion{}, fmt.Errorf("invalid numeric component in %q", raw)
		}
		value, err := strconv.ParseUint(number, 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("invalid numeric component in %q", raw)
		}
		values[i] = value
	}
	result := semanticVersion{major: values[0], minor: values[1], patch: values[2]}
	if len(parts) == 2 {
		if parts[1] == "" {
			return semanticVersion{}, fmt.Errorf("empty prerelease in %q", raw)
		}
		result.pre = strings.Split(parts[1], ".")
		for _, component := range result.pre {
			if component == "" {
				return semanticVersion{}, fmt.Errorf("empty prerelease component in %q", raw)
			}
			for _, character := range component {
				if (character < '0' || character > '9') && (character < 'A' || character > 'Z') &&
					(character < 'a' || character > 'z') && character != '-' {
					return semanticVersion{}, fmt.Errorf("invalid prerelease component in %q", raw)
				}
			}
		}
	}
	return result, nil
}

func compareVersion(a, b semanticVersion) int {
	for _, pair := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}
	for index := 0; index < len(a.pre) && index < len(b.pre); index++ {
		ac, bc := a.pre[index], b.pre[index]
		if ac == bc {
			continue
		}
		an, aerr := strconv.ParseUint(ac, 10, 64)
		bn, berr := strconv.ParseUint(bc, 10, 64)
		switch {
		case aerr == nil && berr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		case ac < bc:
			return -1
		default:
			return 1
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

func versionInRange(raw string, versionRange VersionRange) (bool, error) {
	current, err := parseVersion(raw)
	if err != nil {
		return false, err
	}
	minimum, err := parseVersion(versionRange.Min)
	if err != nil {
		return false, err
	}
	if compareVersion(current, minimum) < 0 {
		return false, nil
	}
	if versionRange.MaxExclusive == "" {
		return true, nil
	}
	maximum, err := parseVersion(versionRange.MaxExclusive)
	if err != nil {
		return false, err
	}
	return compareVersion(current, maximum) < 0, nil
}
