package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// RequestTelemetry holds reasoning mode and conversation attribution extracted
// from an incoming LLM generation request.
type RequestTelemetry struct {
	RequestedModel      string
	OriginalModel       string
	RequestType         string
	ReasoningMode       string
	ThinkingLevel       string
	ThinkingBudget      *int
	IncludeThoughts     *bool
	ConversationID      string
	ConversationSource  string // "cascade_id", "session_id", "content_hash", "user_prompt_id", ""
	ConversationSnippet string
	CascadeID           string
	RootCascadeID       string
	TrajectoryID        string
	UserPromptID        string
}

type telemetryEnvelope struct {
	Request          *telemetryRequest `json:"request"`
	RequestID        string            `json:"requestId"`
	RequestType      string            `json:"requestType"`
	SessionIDSnake   string            `json:"session_id"`
	ConversationID   string            `json:"conversation_id"`
	ConversationIDCC string            `json:"conversationId"`
	UserPromptID     string            `json:"user_prompt_id"`
	UserPromptIDCC   string            `json:"userPromptId"`
	GenerationConfig *thinkingGenCfg   `json:"generationConfig"`
	Contents         firstContentTurn  `json:"contents"`
	Labels           map[string]string `json:"labels"`
}

type telemetryRequest struct {
	GenerationConfig *thinkingGenCfg   `json:"generationConfig"`
	Contents         firstContentTurn  `json:"contents"`
	Labels           map[string]string `json:"labels"`
	SessionIDSnake   string            `json:"session_id"`
	ConversationID   string            `json:"conversation_id"`
	ConversationIDCC string            `json:"conversationId"`
	UserPromptID     string            `json:"user_prompt_id"`
	UserPromptIDCC   string            `json:"userPromptId"`
}

type thinkingGenCfg struct {
	ThinkingConfig *thinkingConfigRaw `json:"thinkingConfig"`
}

type thinkingConfigRaw struct {
	ThinkingLevel   json.RawMessage `json:"thinkingLevel"`
	ThinkingBudget  *int            `json:"thinkingBudget"`
	IncludeThoughts *bool           `json:"includeThoughts"`
}

type contentTurn struct {
	Role  string        `json:"role"`
	Parts []contentPart `json:"parts"`
}

type contentPart struct {
	Text         string          `json:"text"`
	FunctionCall *functionCallID `json:"functionCall"`
	InlineData   *inlineDataMeta `json:"inlineData"`
}

type functionCallID struct {
	Name string `json:"name"`
}

type inlineDataMeta struct {
	MimeType string `json:"mimeType"`
}

// firstContentTurn decodes only contents[0] and skips contents[1..N] in O(1) memory.
type firstContentTurn struct {
	Turn  *contentTurn
	Valid bool
}

// UnmarshalJSON implements json.Unmarshaler for firstContentTurn.
func (f *firstContentTurn) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '[' {
		return nil
	}
	if !dec.More() {
		return nil
	}
	var turn contentTurn
	if err := dec.Decode(&turn); err != nil {
		return nil
	}
	f.Turn = &turn
	f.Valid = true

	// Skip remaining array elements without allocating structs.
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil
		}
		if d, ok := t.(json.Delim); ok {
			switch d {
			case '[', '{':
				depth++
			case ']', '}':
				depth--
			}
		}
	}
	return nil
}

// ExtractRequestTelemetry extracts reasoning mode and conversation attribution
// from request headers, JSON body, and resolved model names.
func ExtractRequestTelemetry(headers http.Header, body []byte, effectiveModel, originalModel string) RequestTelemetry {
	out := RequestTelemetry{
		RequestedModel: effectiveModel,
		OriginalModel:  originalModel,
	}

	var env telemetryEnvelope
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '{' {
		_ = json.Unmarshal(trimmed, &env)
	}

	// Merge nested request fields with top-level fallbacks.
	var genCfg *thinkingGenCfg
	var turn *contentTurn
	var labels map[string]string
	var sessionIDSnake, convID, userPromptID string

	if env.Request != nil {
		genCfg = env.Request.GenerationConfig
		if env.Request.Contents.Valid {
			turn = env.Request.Contents.Turn
		}
		labels = env.Request.Labels
		sessionIDSnake = env.Request.SessionIDSnake
		convID = firstNonEmpty(env.Request.ConversationID, env.Request.ConversationIDCC)
		userPromptID = firstNonEmpty(env.Request.UserPromptID, env.Request.UserPromptIDCC)
	}
	if genCfg == nil {
		genCfg = env.GenerationConfig
	}
	if turn == nil && env.Contents.Valid {
		turn = env.Contents.Turn
	}
	if len(labels) == 0 {
		labels = env.Labels
	}
	if sessionIDSnake == "" {
		sessionIDSnake = env.SessionIDSnake
	}
	if convID == "" {
		convID = firstNonEmpty(env.ConversationID, env.ConversationIDCC)
	}
	if userPromptID == "" {
		userPromptID = firstNonEmpty(env.UserPromptID, env.UserPromptIDCC)
	}
	out.UserPromptID = userPromptID
	out.RequestType = env.RequestType

	// Extract Antigravity cascade / trajectory metadata.
	if len(labels) > 0 {
		out.RootCascadeID = strings.TrimSpace(labels["root_cascade_id"])
		out.CascadeID = strings.TrimSpace(labels["cascade_id"])
		out.TrajectoryID = strings.TrimSpace(labels["trajectory_id"])
	}
	if env.RequestID != "" {
		parts := strings.Split(env.RequestID, "/")
		if len(parts) >= 2 && parts[0] != "" {
			if out.RequestType == "" {
				out.RequestType = parts[0]
			}
			if out.CascadeID == "" {
				out.CascadeID = parts[1]
			}
		}
		if len(parts) >= 4 && out.TrajectoryID == "" {
			out.TrajectoryID = parts[3]
		}
	}

	// Extract reasoning mode and thinking config.
	if genCfg != nil && genCfg.ThinkingConfig != nil {
		tc := genCfg.ThinkingConfig
		out.ThinkingBudget = tc.ThinkingBudget
		out.IncludeThoughts = tc.IncludeThoughts
		out.ThinkingLevel = parseThinkingLevel(tc.ThinkingLevel, tc.ThinkingBudget, effectiveModel)
	}
	out.ReasoningMode = resolveReasoningMode(out.ThinkingLevel, out.ThinkingBudget, out.IncludeThoughts, effectiveModel, originalModel)

	// Extract opening prompt snippet from contents[0].
	if turn != nil {
		out.ConversationSnippet = extractSnippet(turn)
	}

	// Tiered ConversationID resolution.
	switch {
	case out.RootCascadeID != "":
		out.ConversationID = out.RootCascadeID
		out.ConversationSource = "cascade_id"
	case out.CascadeID != "":
		out.ConversationID = out.CascadeID
		out.ConversationSource = "cascade_id"
	case sessionIDSnake != "" || convID != "" || labels["session_id"] != "" || labels["conversation_id"] != "" || headerConvID(headers) != "":
		out.ConversationID = firstNonEmpty(sessionIDSnake, convID, labels["session_id"], labels["conversation_id"], headerConvID(headers))
		out.ConversationSource = "session_id"
	case turn != nil:
		if h := hashFirstTurn(turn); h != "" {
			out.ConversationID = h
			out.ConversationSource = "content_hash"
		}
	}
	if out.ConversationID == "" && out.UserPromptID != "" {
		out.ConversationID = out.UserPromptID
		out.ConversationSource = "user_prompt_id"
	}

	return out
}

func parseThinkingLevel(raw json.RawMessage, budget *int, effectiveModel string) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return strings.ToUpper(strings.TrimSpace(s))
		}
		return ""
	}
	if n, err := strconv.Atoi(string(trimmed)); err == nil {
		if n >= 100 || (n > 0 && strings.HasSuffix(strings.ToLower(effectiveModel), "-xhigh")) {
			return "XHIGH"
		}
		if n == 0 && budget != nil {
			if *budget == 0 {
				return "OFF"
			}
			return "MINIMAL"
		}
		return fmt.Sprintf("level:%d", n)
	}
	return ""
}

func resolveReasoningMode(level string, budget *int, includeThoughts *bool, effectiveModel, originalModel string) string {
	if level != "" {
		return level
	}
	if budget != nil {
		switch {
		case *budget < 0:
			return "auto"
		case *budget == 0:
			return "off"
		default:
			return fmt.Sprintf("budget:%d", *budget)
		}
	}
	for _, m := range []string{effectiveModel, originalModel} {
		if mode := modelSuffixReasoning(m); mode != "" {
			return mode
		}
	}
	if includeThoughts != nil && *includeThoughts {
		return "THINKING"
	}
	return ""
}

func modelSuffixReasoning(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasSuffix(m, "-xhigh"):
		return "XHIGH"
	case strings.HasSuffix(m, "-high"):
		return "HIGH"
	case strings.HasSuffix(m, "-medium"), strings.HasSuffix(m, "-med"):
		return "MEDIUM"
	case strings.HasSuffix(m, "-low"):
		return "LOW"
	case strings.HasSuffix(m, "-minimal"):
		return "MINIMAL"
	case strings.HasSuffix(m, "-thinking"):
		return "THINKING"
	default:
		return ""
	}
}

func headerConvID(h http.Header) string {
	if h == nil {
		return ""
	}
	return firstNonEmpty(strings.TrimSpace(h.Get("X-Conversation-Id")), strings.TrimSpace(h.Get("X-Session-Id")))
}

func hashFirstTurn(turn *contentTurn) string {
	if turn == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(turn.Role)
	hasContent := false
	for _, p := range turn.Parts {
		if p.Text != "" {
			b.WriteString("|t:")
			b.WriteString(p.Text)
			hasContent = true
		}
		if p.FunctionCall != nil && p.FunctionCall.Name != "" {
			b.WriteString("|fc:")
			b.WriteString(p.FunctionCall.Name)
			hasContent = true
		}
		if p.InlineData != nil && p.InlineData.MimeType != "" {
			b.WriteString("|id:")
			b.WriteString(p.InlineData.MimeType)
			hasContent = true
		}
	}
	if !hasContent {
		return ""
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "c-" + hex.EncodeToString(sum[:4])
}

func extractSnippet(turn *contentTurn) string {
	if turn == nil {
		return ""
	}
	for _, p := range turn.Parts {
		if strings.TrimSpace(p.Text) == "" {
			continue
		}
		// Collapse whitespace/newlines into single spaces.
		var b strings.Builder
		inSpace := true
		runeCount := 0
		for _, r := range p.Text {
			if unicode.IsSpace(r) {
				if !inSpace && runeCount < 60 {
					b.WriteByte(' ')
					runeCount++
					inSpace = true
				}
				continue
			}
			if runeCount >= 60 {
				break
			}
			b.WriteRune(r)
			runeCount++
			inSpace = false
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
