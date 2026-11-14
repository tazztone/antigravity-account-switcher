package proxy

import (
	"net/http"
	"strings"
	"testing"
)

func TestExtractRequestTelemetry_ThinkingLevelPolymorphic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		body      string
		model     string
		wantLevel string
		wantMode  string
	}{
		{
			name:      "string_low",
			body:      `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"low"}}}}`,
			model:     "gemini-4-hydrogen-low",
			wantLevel: "LOW",
			wantMode:  "LOW",
		},
		{
			name:      "string_high",
			body:      `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"HIGH"}}}}`,
			model:     "claude-sonnet-5-5-high",
			wantLevel: "HIGH",
			wantMode:  "HIGH",
		},
		{
			name:      "numeric_100_xhigh",
			body:      `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":100}}}}`,
			model:     "gemini-4-hydrogen-xhigh",
			wantLevel: "XHIGH",
			wantMode:  "XHIGH",
		},
		{
			name:      "numeric_other",
			body:      `{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":50}}}}`,
			model:     "gemini-4-hydrogen",
			wantLevel: "level:50",
			wantMode:  "level:50",
		},
		{
			name:      "budget_positive",
			body:      `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192,"includeThoughts":true}}}}`,
			model:     "gemini-2.5-pro",
			wantLevel: "",
			wantMode:  "budget:8192",
		},
		{
			name:      "budget_auto",
			body:      `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}}`,
			model:     "gemini-2.5-flash",
			wantLevel: "",
			wantMode:  "auto",
		},
		{
			name:      "budget_off",
			body:      `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}}`,
			model:     "gemini-2.5-flash",
			wantLevel: "",
			wantMode:  "off",
		},
		{
			name:      "model_suffix_fallback",
			body:      `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			model:     "claude-sonnet-5-5-med",
			wantLevel: "",
			wantMode:  "MEDIUM",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractRequestTelemetry(nil, []byte(tc.body), tc.model, tc.model)
			if got.ThinkingLevel != tc.wantLevel {
				t.Fatalf("ThinkingLevel = %q, want %q", got.ThinkingLevel, tc.wantLevel)
			}
			if got.ReasoningMode != tc.wantMode {
				t.Fatalf("ReasoningMode = %q, want %q", got.ReasoningMode, tc.wantMode)
			}
		})
	}
}

func TestExtractRequestTelemetry_ConversationPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("root_cascade_id_over_static_sessionId", func(t *testing.T) {
		t.Parallel()
		body := `{
			"requestId": "agent/11111111-1111-1111-1111-111111111111/1700000000/traj-1/0",
			"request": {
				"sessionId": "-3750763034362895579",
				"labels": {
					"root_cascade_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
					"cascade_id": "11111111-1111-1111-1111-111111111111",
					"trajectory_id": "traj-1"
				},
				"contents": [{"role":"user","parts":[{"text":"hello\nworld   test"}]}]
			}
		}`
		got := ExtractRequestTelemetry(nil, []byte(body), "gemini-4-hydrogen-high", "gemini-4-hydrogen-high")
		if got.ConversationID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
			t.Fatalf("ConversationID = %q", got.ConversationID)
		}
		if got.ConversationSource != "cascade_id" {
			t.Fatalf("ConversationSource = %q, want cascade_id", got.ConversationSource)
		}
		if got.RequestType != "agent" || got.TrajectoryID != "traj-1" {
			t.Fatalf("RequestType=%q TrajectoryID=%q", got.RequestType, got.TrajectoryID)
		}
		if got.ConversationSnippet != "hello world test" {
			t.Fatalf("ConversationSnippet = %q", got.ConversationSnippet)
		}
	})

	t.Run("snake_case_session_id_ignores_camel_case_sessionId", func(t *testing.T) {
		t.Parallel()
		body := `{"request":{"sessionId":"-3750763034362895579","session_id":"sess-abc-123"}}`
		got := ExtractRequestTelemetry(nil, []byte(body), "gemini-2.5-pro", "gemini-2.5-pro")
		if got.ConversationID != "sess-abc-123" || got.ConversationSource != "session_id" {
			t.Fatalf("got (%q, %q), want (sess-abc-123, session_id)", got.ConversationID, got.ConversationSource)
		}
	})

	t.Run("header_session_id", func(t *testing.T) {
		t.Parallel()
		h := http.Header{}
		h.Set("X-Conversation-Id", "hdr-conv-42")
		body := `{"request":{"sessionId":"-3750763034362895579"}}`
		got := ExtractRequestTelemetry(h, []byte(body), "gemini-2.5-pro", "gemini-2.5-pro")
		if got.ConversationID != "hdr-conv-42" || got.ConversationSource != "session_id" {
			t.Fatalf("got (%q, %q)", got.ConversationID, got.ConversationSource)
		}
	})

	t.Run("content_hash_stable_across_systemInstruction_and_later_turns", func(t *testing.T) {
		t.Parallel()
		body1 := `{
			"request": {
				"sessionId": "-3750763034362895579",
				"systemInstruction": {"parts":[{"text":"prompt v1"}]},
				"contents": [
					{"role":"user","parts":[{"text":"fix bug in parser"}]}
				]
			}
		}`
		body2 := `{
			"request": {
				"sessionId": "-3750763034362895579",
				"systemInstruction": {"parts":[{"text":"prompt v2"}]},
				"contents": [
					{"role":"user","parts":[{"text":"fix bug in parser"}]},
					{"role":"model","parts":[{"text":"sure","thoughtSignature":"sig-xyz"}]},
					{"role":"user","parts":[{"text":"next turn"}]}
				]
			}
		}`
		g1 := ExtractRequestTelemetry(nil, []byte(body1), "gemini-2.5-pro", "gemini-2.5-pro")
		g2 := ExtractRequestTelemetry(nil, []byte(body2), "gemini-2.5-pro", "gemini-2.5-pro")
		if g1.ConversationSource != "content_hash" || !strings.HasPrefix(g1.ConversationID, "c-") {
			t.Fatalf("unexpected hash result: %+v", g1)
		}
		if g1.ConversationID != g2.ConversationID {
			t.Fatalf("content_hash drifted: %q != %q", g1.ConversationID, g2.ConversationID)
		}
	})
}
