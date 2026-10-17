package v1

// Media is the library-independent attachment descriptor. FileID is an opaque
// core handle, not a Telegram-library object; plugins request downloads through
// an explicit client operation.
type Media struct {
	Kind string `json:"kind"`
	// Handle is opaque to plugins and is the only media reference accepted by
	// media operations. FileID is retained as a v1 compatibility alias.
	Handle     string           `json:"handle,omitempty"`
	FileID     string           `json:"file_id,omitempty"`
	FileName   string           `json:"file_name,omitempty"`
	MIMEType   string           `json:"mime_type,omitempty"`
	Size       int64            `json:"size,omitempty"`
	Checksum   string           `json:"checksum,omitempty"`
	Source     MediaOwner       `json:"source,omitempty"`
	Attributes *MediaAttributes `json:"attributes,omitempty"`
}

type MediaOwner struct {
	PluginID  string `json:"plugin_id,omitempty"`
	AccountID int64  `json:"account_id,omitempty"`
	PeerID    int64  `json:"peer_id,omitempty"`
}

type MediaAttributes struct {
	Width           int  `json:"width,omitempty"`
	Height          int  `json:"height,omitempty"`
	DurationSeconds int  `json:"duration_seconds,omitempty"`
	Animated        bool `json:"animated,omitempty"`
}

type MediaAttachment struct {
	Handle     string           `json:"handle"`
	Kind       string           `json:"kind"`
	MIMEType   string           `json:"mime_type,omitempty"`
	FileName   string           `json:"file_name,omitempty"`
	Size       int64            `json:"size,omitempty"`
	Checksum   string           `json:"checksum,omitempty"`
	Caption    string           `json:"caption,omitempty"`
	ParseMode  string           `json:"parse_mode,omitempty"`
	Attributes *MediaAttributes `json:"attributes,omitempty"`
}

type MediaOperation string

const (
	MediaInspect  MediaOperation = "inspect"
	MediaDownload MediaOperation = "download"
	MediaUpload   MediaOperation = "upload"
	MediaAttach   MediaOperation = "attach"
	MediaDelete   MediaOperation = "delete"
)

type MediaRequest struct {
	Operation MediaOperation `json:"operation"`
	Handle    string         `json:"handle,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	MIMEType  string         `json:"mime_type,omitempty"`
	FileName  string         `json:"file_name,omitempty"`
	Size      int64          `json:"size,omitempty"`
	Checksum  string         `json:"checksum,omitempty"`
	PeerID    int64          `json:"peer_id,omitempty"`
	// Offset, Data, and Complete are bounded transfer fields used by the
	// supervised Go/Python host bridge. Data is limited to one media chunk and
	// is never accepted for inspect/attach/delete operations.
	Offset   int64  `json:"offset,omitempty"`
	Data     []byte `json:"data,omitempty"`
	Complete bool   `json:"complete,omitempty"`
}

type MediaResult struct {
	Media      *Media `json:"media,omitempty"`
	TransferID string `json:"transfer_id,omitempty"`
	ChunkSize  int    `json:"chunk_size,omitempty"`
	MaxSize    int64  `json:"max_size,omitempty"`
	Complete   bool   `json:"complete,omitempty"`
	// Data is one bounded download chunk. It is present only on the internal
	// supervised media bridge and is never larger than MediaChunkSizeBytes.
	Data []byte `json:"data,omitempty"`
}
