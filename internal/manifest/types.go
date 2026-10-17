package manifest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-faster/yaml"
)

const (
	APIVersion   = "serenox.app/v1"
	Kind         = "Application"
	MaxFileBytes = 1 << 20
)

var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// SecretRef is a reference to an operator-owned secret. Plaintext secret
// values are intentionally not representable in the manifest DTO.
type SecretRef struct {
	env  string
	file string
}

func (r SecretRef) IsZero() bool { return r.env == "" && r.file == "" }
func (r SecretRef) Env() string  { return r.env }
func (r SecretRef) File() string { return r.file }

func (r SecretRef) String() string {
	if r.env != "" {
		return "${" + r.env + "}"
	}
	if r.file != "" {
		return "file:" + r.file
	}
	return ""
}

func (r SecretRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.String())
}

func (r SecretRef) MarshalYAML() (any, error) { return r.String(), nil }

func (r *SecretRef) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return fmt.Errorf("secret reference is empty")
	}
	switch node.Kind {
	case yaml.ScalarNode:
		if node.ShortTag() != "!!str" {
			return fmt.Errorf("secret reference must be a string or {env,file} object")
		}
		parsed, err := ParseSecretRef(node.Value)
		if err != nil {
			return err
		}
		*r = parsed
		return nil
	case yaml.MappingNode:
		if len(node.Content) != 2 {
			return fmt.Errorf("secret reference object must contain exactly one field")
		}
		key, value := node.Content[0], node.Content[1]
		if key.Kind != yaml.ScalarNode || value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
			return fmt.Errorf("secret reference object must contain a string env or file field")
		}
		if key.Value != "env" && key.Value != "file" {
			return fmt.Errorf("secret reference object has unknown field %q", key.Value)
		}
		if key.Value == "env" {
			if !envNamePattern.MatchString(value.Value) {
				return fmt.Errorf("secret environment name is invalid")
			}
			*r = SecretRef{env: value.Value}
			return nil
		}
		if strings.TrimSpace(value.Value) == "" || strings.ContainsRune(value.Value, '\x00') {
			return fmt.Errorf("secret file path is invalid")
		}
		*r = SecretRef{file: value.Value}
		return nil
	default:
		return fmt.Errorf("secret reference must be a string or object")
	}
}

// IntInput permits a bounded integer or an environment secret reference. It
// is used for API IDs, whose value is not itself sensitive but is often
// injected alongside Telegram credentials.
type IntInput struct {
	Value string
	Ref   SecretRef
}

func (v IntInput) IsZero() bool { return v.Value == "" && v.Ref.IsZero() }

func (v *IntInput) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("integer input must be a scalar")
	}
	if node.ShortTag() == "!!int" {
		v.Value = node.Value
		v.Ref = SecretRef{}
		return nil
	}
	if node.ShortTag() != "!!str" {
		return fmt.Errorf("integer input must be an integer or environment reference")
	}
	if strings.HasPrefix(node.Value, "${") {
		ref, err := ParseSecretRef(node.Value)
		if err != nil {
			return err
		}
		v.Value = ""
		v.Ref = ref
		return nil
	}
	if !regexp.MustCompile(`^[0-9]{1,10}$`).MatchString(node.Value) {
		return fmt.Errorf("integer input must contain only decimal digits")
	}
	v.Value = node.Value
	v.Ref = SecretRef{}
	return nil
}

func (v IntInput) MarshalJSON() ([]byte, error) {
	if !v.Ref.IsZero() {
		return v.Ref.MarshalJSON()
	}
	return json.Marshal(v.Value)
}

func (v IntInput) MarshalYAML() (any, error) {
	if !v.Ref.IsZero() {
		return v.Ref.String(), nil
	}
	return v.Value, nil
}

// Duration is deliberately string-only to reject YAML's ambiguous numeric
// scalar coercions.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" {
		return fmt.Errorf("duration must be a string")
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }
func (d Duration) MarshalYAML() (any, error)    { return d.String(), nil }

type Document struct {
	APIVersion    string             `yaml:"apiVersion" json:"apiVersion"`
	Kind          string             `yaml:"kind" json:"kind"`
	Metadata      Metadata           `yaml:"metadata" json:"metadata"`
	Runtime       Runtime            `yaml:"runtime" json:"runtime"`
	Telegram      Telegram           `yaml:"telegram" json:"telegram"`
	Storage       Storage            `yaml:"storage" json:"storage"`
	Services      map[string]Service `yaml:"services" json:"services"`
	Plugins       []PluginRef        `yaml:"plugins" json:"plugins"`
	Release       Release            `yaml:"release" json:"release"`
	Observability *Observability     `yaml:"observability,omitempty" json:"observability,omitempty"`
	Backup        *Backup            `yaml:"backup,omitempty" json:"backup,omitempty"`
	Deployment    *Deployment        `yaml:"deployment,omitempty" json:"deployment,omitempty"`
	present       map[string]bool
}

type Metadata struct {
	Name    string `yaml:"name" json:"name"`
	Version int    `yaml:"version" json:"version"`
}

type Runtime struct {
	DataDir         string   `yaml:"dataDir" json:"dataDir"`
	ConfigMode      string   `yaml:"configMode" json:"configMode"`
	LogLevel        string   `yaml:"logLevel" json:"logLevel"`
	ShutdownTimeout Duration `yaml:"shutdownTimeout" json:"shutdownTimeout"`
	SecretDir       string   `yaml:"secretDir,omitempty" json:"secretDir,omitempty"`
	QueueCapacity   int      `yaml:"queueCapacity,omitempty" json:"queueCapacity,omitempty"`
	Workers         int      `yaml:"workers,omitempty" json:"workers,omitempty"`
	HandlerTimeout  Duration `yaml:"handlerTimeout,omitempty" json:"handlerTimeout,omitempty"`
}

type Telegram struct {
	Enabled     bool      `yaml:"enabled" json:"enabled"`
	APIID       IntInput  `yaml:"apiId" json:"apiId"`
	APIHash     SecretRef `yaml:"apiHash" json:"apiHash"`
	SessionFile string    `yaml:"sessionFile" json:"sessionFile"`
	AuthMode    string    `yaml:"authMode,omitempty" json:"authMode,omitempty"`
}

type Storage struct {
	Backend             string `yaml:"backend" json:"backend"`
	Path                string `yaml:"path,omitempty" json:"path,omitempty"`
	Service             string `yaml:"service,omitempty" json:"service,omitempty"`
	MaxNamespaces       int    `yaml:"maxNamespaces,omitempty" json:"maxNamespaces,omitempty"`
	MaxKeysPerNamespace int    `yaml:"maxKeysPerNamespace,omitempty" json:"maxKeysPerNamespace,omitempty"`
	MaxTotalEntries     int    `yaml:"maxTotalEntries,omitempty" json:"maxTotalEntries,omitempty"`
	MaxNamespaceBytes   int    `yaml:"maxNamespaceBytes,omitempty" json:"maxNamespaceBytes,omitempty"`
	MaxKeyBytes         int    `yaml:"maxKeyBytes,omitempty" json:"maxKeyBytes,omitempty"`
	MaxValueBytes       int    `yaml:"maxValueBytes,omitempty" json:"maxValueBytes,omitempty"`
	MaxFileBytes        int64  `yaml:"maxFileBytes,omitempty" json:"maxFileBytes,omitempty"`
}

type Service struct {
	Kind           string    `yaml:"kind" json:"kind"`
	Required       bool      `yaml:"required" json:"required"`
	Endpoint       string    `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Credentials    SecretRef `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	TLS            TLS       `yaml:"tls,omitempty" json:"tls,omitempty"`
	ConnectTimeout Duration  `yaml:"connectTimeout,omitempty" json:"connectTimeout,omitempty"`
	RequestTimeout Duration  `yaml:"requestTimeout,omitempty" json:"requestTimeout,omitempty"`
	MaxConnections int       `yaml:"maxConnections,omitempty" json:"maxConnections,omitempty"`
	MaxRetries     int       `yaml:"maxRetries,omitempty" json:"maxRetries,omitempty"`
	MaxBytes       int64     `yaml:"maxBytes,omitempty" json:"maxBytes,omitempty"`
	MaxRequests    int       `yaml:"maxRequests,omitempty" json:"maxRequests,omitempty"`
	PrivateNetwork bool      `yaml:"privateNetwork,omitempty" json:"privateNetwork,omitempty"`
	Bucket         string    `yaml:"bucket,omitempty" json:"bucket,omitempty"`
	Prefix         string    `yaml:"prefix,omitempty" json:"prefix,omitempty"`
}

type TLS struct {
	Mode   string `yaml:"mode,omitempty" json:"mode,omitempty"`
	CAFile string `yaml:"caFile,omitempty" json:"caFile,omitempty"`
}

type PluginRef struct {
	ID      string `yaml:"id" json:"id"`
	Path    string `yaml:"path" json:"path"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
}

type Release struct {
	RetainVersions int      `yaml:"retainVersions" json:"retainVersions"`
	HealthTimeout  Duration `yaml:"healthTimeout" json:"healthTimeout"`
}

type Observability struct {
	JSONLogs        bool `yaml:"jsonLogs,omitempty" json:"jsonLogs,omitempty"`
	MaxMetricSeries int  `yaml:"maxMetricSeries,omitempty" json:"maxMetricSeries,omitempty"`
}

type Backup struct {
	Directory string `yaml:"directory,omitempty" json:"directory,omitempty"`
	MaxBytes  int64  `yaml:"maxBytes,omitempty" json:"maxBytes,omitempty"`
}

type Deployment struct {
	ServiceName        string `yaml:"serviceName,omitempty" json:"serviceName,omitempty"`
	User               string `yaml:"user,omitempty" json:"user,omitempty"`
	WorkingDirectory   string `yaml:"workingDirectory,omitempty" json:"workingDirectory,omitempty"`
	EnvironmentFile    string `yaml:"environmentFile,omitempty" json:"environmentFile,omitempty"`
	PersistentDataPath string `yaml:"persistentDataPath,omitempty" json:"persistentDataPath,omitempty"`
	Image              string `yaml:"image,omitempty" json:"image,omitempty"`
	ComposeProject     string `yaml:"composeProject,omitempty" json:"composeProject,omitempty"`
}

func ParseSecretRef(value string) (SecretRef, error) {
	if len(value) < 4 || !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") || strings.Count(value, "${") != 1 {
		return SecretRef{}, fmt.Errorf("secret must be an exact ${ENV_NAME} reference")
	}
	name := value[2 : len(value)-1]
	if !envNamePattern.MatchString(name) {
		return SecretRef{}, fmt.Errorf("secret environment name is invalid")
	}
	return SecretRef{env: name}, nil
}
