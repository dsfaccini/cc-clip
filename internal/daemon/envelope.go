package daemon

import "time"

// NotifyKind discriminates the payload carried by a NotifyEnvelope.
type NotifyKind string

const (
	KindImageTransfer  NotifyKind = "image_transfer"
	KindToolAttention  NotifyKind = "tool_attention"
	KindGenericMessage NotifyKind = "generic_message"
)

// NotifyEnvelope is the unified notification model. For KindToolAttention,
// both ToolAttention and GenericMessage may be set (GenericMessage carries
// display-ready text derived by the classifier). For other kinds, exactly
// one payload field is non-nil, matching Kind.
type NotifyEnvelope struct {
	Kind   NotifyKind
	Source string
	Host   string
	// Target is the agent CLI the notification came from (see
	// ReceiptTargets). Unlike Host it is self-declared by the sender: it only
	// attributes a delivery receipt and never gates delivery.
	Target    string
	Timestamp time.Time
	// OnClick is a shell command the macOS notifier runs when the user clicks
	// the notification. Only the daemon sets it: no /notify field maps to it.
	OnClick string

	ImageTransfer  *ImageTransferPayload
	ToolAttention  *ToolAttentionPayload
	GenericMessage *GenericMessagePayload
}

// ImageTransferPayload carries clipboard image transfer metadata.
type ImageTransferPayload struct {
	SessionID   string
	Seq         int
	Fingerprint string
	ImageData   []byte
	Format      string
	Width       int
	Height      int
	DuplicateOf int
}

// ToolAttentionPayload carries hook/tool attention metadata (future use).
type ToolAttentionPayload struct {
	SessionID  string
	HookType   string
	StopReason string
	NotifType  string
	ToolName   string
	ToolInput  string
	Message    string
	Verified   bool
}

// GenericMessagePayload carries a freeform notification (future use).
type GenericMessagePayload struct {
	Title      string
	Body       string
	Urgency    int
	Verified   bool
	Sound      string
	Subtitle   string
	DedupCount int
}
