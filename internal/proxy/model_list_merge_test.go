package proxy

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
)

// decodeMergedModelList decodes a merged response and returns the models map.
func decodeMergedModelList(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("merged response is not valid JSON: %v (%s)", err, body)
	}
	return doc.Models
}

// modelIDsOf returns the sorted keys of a models map.
func modelIDsOf(models map[string]json.RawMessage) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// sortModelIDs returns every model ID referenced by the agentModelSorts of a body.
func sortModelIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var doc struct {
		AgentModelSorts []struct {
			DisplayName string `json:"displayName"`
			Groups      []struct {
				ModelIDs []string `json:"modelIds"`
			} `json:"groups"`
		} `json:"agentModelSorts"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("merged response has invalid agentModelSorts: %v", err)
	}
	var ids []string
	for _, entry := range doc.AgentModelSorts {
		for _, group := range entry.Groups {
			ids = append(ids, group.ModelIDs...)
		}
	}
	return ids
}

func TestMergeModelListResponses_UnionsDisjointCatalogs(t *testing.T) {
	base := []byte(`{
		"models": {"claude-sonnet-4-6": {"displayName": "Claude Sonnet 4.6", "recommended": true}},
		"defaultAgentModelId": "claude-sonnet-4-6",
		"agentModelSorts": [{"displayName": "Recommended", "groups": [{"modelIds": ["claude-sonnet-4-6"]}]}]
	}`)
	other := []byte(`{
		"models": {
			"gemini-2.5-pro": {"displayName": "Gemini 2.5 Pro"},
			"claude-sonnet-4-6": {"displayName": "Claude Sonnet 4.6"}
		},
		"defaultAgentModelId": "gemini-2.5-pro",
		"agentModelSorts": [{"displayName": "Recommended", "groups": [{"modelIds": ["gemini-2.5-pro", "claude-sonnet-4-6"]}]}]
	}`)

	merged, err := MergeModelListResponses([][]byte{base, other})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}

	models := decodeMergedModelList(t, merged)
	want := []string{"claude-sonnet-4-6", "gemini-2.5-pro"}
	if got := modelIDsOf(models); !reflect.DeepEqual(got, want) {
		t.Errorf("merged models = %v, want %v", got, want)
	}

	var doc struct {
		DefaultAgentModelID string `json:"defaultAgentModelId"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("unmarshal default model: %v", err)
	}
	if doc.DefaultAgentModelID != "claude-sonnet-4-6" {
		t.Errorf("defaultAgentModelId = %q, want base value %q", doc.DefaultAgentModelID, "claude-sonnet-4-6")
	}

	if ids := sortModelIDs(t, merged); !reflect.DeepEqual(ids, want) {
		t.Errorf("sorted models = %v, want %v", ids, want)
	}
}

func TestMergeModelListResponses_PrefersEnabledModelEntry(t *testing.T) {
	base := []byte(`{"models": {"claude-sonnet-4-6": {"displayName": "Claude Sonnet 4.6", "disabled": true}}}`)
	other := []byte(`{"models": {"claude-sonnet-4-6": {"displayName": "Claude Sonnet 4.6", "disabled": false, "maxTokens": 8192}}}`)

	merged, err := MergeModelListResponses([][]byte{base, other})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}

	models := decodeMergedModelList(t, merged)
	var entry struct {
		Disabled  bool `json:"disabled"`
		MaxTokens int  `json:"maxTokens"`
	}
	if err := json.Unmarshal(models["claude-sonnet-4-6"], &entry); err != nil {
		t.Fatalf("unmarshal model entry: %v", err)
	}
	if entry.Disabled {
		t.Error("merged entry is disabled, want the enabled entry to win")
	}
	if entry.MaxTokens != 8192 {
		t.Errorf("unknown model fields were dropped: maxTokens = %d, want 8192", entry.MaxTokens)
	}
}

func TestMergeModelListResponses_MergesOrderingByDisplayName(t *testing.T) {
	base := []byte(`{
		"models": {"a": {}, "b": {}},
		"agentModelSorts": [
			{"displayName": "Recommended", "groups": [{"modelIds": ["a"]}]},
			{"displayName": "More models", "groups": [{"modelIds": ["b"]}]}
		]
	}`)
	other := []byte(`{
		"models": {"a": {}, "c": {}, "p": {}},
		"agentModelSorts": [
			{"displayName": "recommended", "groups": [{"modelIds": ["a", "c"]}]},
			{"displayName": "Preview", "groups": [{"modelIds": ["p"]}]}
		]
	}`)

	merged, err := MergeModelListResponses([][]byte{base, other})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}

	var doc struct {
		AgentModelSorts []struct {
			DisplayName string `json:"displayName"`
			Groups      []struct {
				ModelIDs []string `json:"modelIds"`
			} `json:"groups"`
		} `json:"agentModelSorts"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("unmarshal agentModelSorts: %v", err)
	}

	var names []string
	for _, entry := range doc.AgentModelSorts {
		names = append(names, entry.DisplayName)
	}
	if want := []string{"Recommended", "More models", "Preview"}; !reflect.DeepEqual(names, want) {
		t.Errorf("sort display names = %v, want %v", names, want)
	}

	recommended := doc.AgentModelSorts[0]
	var recommendedIDs []string
	for _, group := range recommended.Groups {
		recommendedIDs = append(recommendedIDs, group.ModelIDs...)
	}
	if want := []string{"a", "c"}; !reflect.DeepEqual(recommendedIDs, want) {
		t.Errorf("Recommended group ids = %v, want %v", recommendedIDs, want)
	}
}

func TestMergeModelListResponses_ExcludesUnsortedAndInternalModelsFromPickerOrdering(t *testing.T) {
	base := []byte(`{
		"models": {
			"claude-sonnet-4-6": {"displayName": "Claude Sonnet 4.6", "recommended": true},
			"chat_20706": {"displayName": ""},
			"gemini-3.5-flash-lite": {"displayName": "Gemini 3.5 Flash Lite"},
			"gemini-3.5-flash-lite-preview": {"displayName": "Gemini 3.5 Flash Lite"},
			"gemini-3.8-flash-tiered": {"displayName": "Gemini 3.8 Flash"}
		},
		"agentModelSorts": [{"displayName": "Recommended", "groups": [{"modelIds": ["claude-sonnet-4-6"]}]}]
	}`)
	other := []byte(`{
		"models": {
			"claude-opus-5.5-high": {"displayName": "Claude Opus 5.5 (High)"},
			"gemini-3-flash": {"displayName": "Gemini 3 Flash"},
			"gemini-3.1-flash-image": {"displayName": "Gemini 3.1 Flash Image"},
			"gemini-4-argon-disabled": {"displayName": "Gemini 4 Argon", "disabled": true}
		},
		"agentModelSorts": [{
			"displayName": "Recommended",
			"groups": [
				{"modelIds": ["claude-opus-5.5-high"]},
				{"modelIds": ["gemini-4-argon-disabled"]}
			]
		}]
	}`)

	merged, err := MergeModelListResponses([][]byte{base, other})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}

	// models map contains union of models
	allModels := decodeMergedModelList(t, merged)
	if _, ok := allModels["claude-opus-5.5-high"]; !ok {
		t.Error("expected claude-opus-5.5-high in models map")
	}
	if _, ok := allModels["chat_20706"]; !ok {
		t.Error("expected chat_20706 retained in backend models map")
	}

	// agentModelSorts must only contain user-selectable agent models, never internal/disabled leftovers
	ids := sortModelIDs(t, merged)
	want := []string{"claude-sonnet-4-6", "claude-opus-5.5-high"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("sorted picker models = %v, want strictly curated models %v", ids, want)
	}
}

func TestMergeModelListResponses_SingleResponsePassesThrough(t *testing.T) {
	single := []byte(`{
		"models": {"gemini-2.5-pro": {"displayName": "Gemini 2.5 Pro", "recommended": true}},
		"defaultAgentModelId": "gemini-2.5-pro",
		"agentModelSorts": [{"displayName": "Recommended", "groups": [{"modelIds": ["gemini-2.5-pro"]}]}]
	}`)

	merged, err := MergeModelListResponses([][]byte{single})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}

	var got, want map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if err := json.Unmarshal(single, &want); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single response was rewritten:\n got: %v\nwant: %v", got, want)
	}
}

func TestMergeModelListResponses_SkipsUnusableBodies(t *testing.T) {
	valid := []byte(`{"models": {"gemini-2.5-pro": {"displayName": "Gemini 2.5 Pro"}}}`)

	merged, err := MergeModelListResponses([][]byte{
		[]byte(`not json at all`),
		[]byte(`[]`),
		[]byte(`{"models": {}}`),
		[]byte(`null`),
		valid,
	})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}
	if got := modelIDsOf(decodeMergedModelList(t, merged)); !reflect.DeepEqual(got, []string{"gemini-2.5-pro"}) {
		t.Errorf("merged models = %v, want only the valid response", got)
	}

	if _, err := MergeModelListResponses([][]byte{[]byte(`garbage`), []byte(`{"models": {}}`)}); !errors.Is(err, ErrNoModelListBodies) {
		t.Errorf("expected ErrNoModelListBodies, got %v", err)
	}
}

func TestMergeModelListResponses_PreservesResponseEnvelope(t *testing.T) {
	wrapped := []byte(`{"response": {"models": {"a": {"displayName": "A"}}, "defaultAgentModelId": "a"}}`)
	unwrapped := []byte(`{"models": {"b": {"displayName": "B"}}}`)

	merged, err := MergeModelListResponses([][]byte{wrapped, unwrapped})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}

	var doc struct {
		Response *struct {
			Models              map[string]json.RawMessage `json:"models"`
			DefaultAgentModelID string                     `json:"defaultAgentModelId"`
		} `json:"response"`
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("unmarshal merged envelope: %v", err)
	}
	if doc.Response == nil {
		t.Fatal("response envelope was lost during the merge")
	}
	if len(doc.Models) != 0 {
		t.Errorf("unexpected top-level models outside the envelope: %v", modelIDsOf(doc.Models))
	}
	if got := modelIDsOf(doc.Response.Models); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("enveloped models = %v, want [a b]", got)
	}
	if doc.Response.DefaultAgentModelID != "a" {
		t.Errorf("enveloped defaultAgentModelId = %q, want %q", doc.Response.DefaultAgentModelID, "a")
	}
}

func TestMergeModelListResponses_PreservesUnknownPayloadFields(t *testing.T) {
	base := []byte(`{"models": {"a": {}}, "quotaTier": "pro", "modelsVersion": 7}`)
	other := []byte(`{"models": {"b": {}}, "quotaTier": "free", "betaFlag": true}`)

	merged, err := MergeModelListResponses([][]byte{base, other})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if string(doc["quotaTier"]) != `"pro"` {
		t.Errorf("unknown payload field was overridden: quotaTier = %s", doc["quotaTier"])
	}
	if string(doc["modelsVersion"]) != "7" {
		t.Errorf("unknown payload field was dropped: modelsVersion = %s", doc["modelsVersion"])
	}
	if string(doc["betaFlag"]) != "true" {
		t.Errorf("field only present in a later response was dropped: betaFlag = %s", doc["betaFlag"])
	}
}
