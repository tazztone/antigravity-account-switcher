package proxy

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// ErrNoModelListBodies indicates that none of the supplied upstream responses
// carried a mergeable fetchAvailableModels payload.
var ErrNoModelListBodies = errors.New("no model list payloads to merge")

// modelListDocument is one decoded fetchAvailableModels response.
//
// Fields are kept as raw JSON so anything this package does not model (per-model
// capability flags, future envelope additions) survives the merge byte-for-byte.
// Only the models map and agentModelSorts ordering are rewritten.
type modelListDocument struct {
	top     map[string]json.RawMessage
	payload map[string]json.RawMessage
	wrapped bool
}

// modelSortEntry is one agentModelSorts element, keeping unknown fields intact.
type modelSortEntry struct {
	raw         map[string]json.RawMessage
	displayName string
	groups      []json.RawMessage
	dirty       bool
}

// MergeModelListResponses merges several fetchAvailableModels JSON responses into one
// response exposing the union of all models.
//
// The first body carrying a non-empty models map is the base: its defaultAgentModelId
// and envelope shape ("response" wrapper or top-level) are authoritative. Later bodies
// contribute models missing from the base and extend the agentModelSorts ordering.
func MergeModelListResponses(bodies [][]byte) ([]byte, error) {
	var base *modelListDocument
	merged := 0

	for _, body := range bodies {
		doc, err := decodeModelListDocument(body)
		if err != nil {
			continue
		}
		models := doc.models()
		if len(models) == 0 {
			continue
		}
		if base == nil {
			base = doc
			merged = 1
			continue
		}
		base.merge(doc, models)
		merged++
	}

	if base == nil {
		return nil, ErrNoModelListBodies
	}
	if merged > 1 {
		base.ensureAllModelsSorted()
	}
	return base.marshal()
}

// decodeModelListDocument parses a response body, unwrapping the optional
// {"response": {...}} language_server envelope while remembering it for output.
func decodeModelListDocument(body []byte) (*modelListDocument, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	doc := &modelListDocument{top: top, payload: top}
	if raw, ok := top["response"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err == nil && len(nested) > 0 {
			doc.payload = nested
			doc.wrapped = true
		}
	}
	return doc, nil
}

// models returns the decoded models map, or nil when the payload has none.
func (d *modelListDocument) models() map[string]json.RawMessage {
	if d == nil || d.payload == nil {
		return nil
	}
	raw, ok := d.payload["models"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var models map[string]json.RawMessage
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil
	}
	return models
}

// merge unions the models map and ordering of other into d.
func (d *modelListDocument) merge(other *modelListDocument, otherModels map[string]json.RawMessage) {
	ownModels := d.models()
	if ownModels == nil {
		ownModels = make(map[string]json.RawMessage, len(otherModels))
	}
	for id, raw := range otherModels {
		if existing, ok := ownModels[id]; ok && !isDisabledModelEntry(existing) {
			// The base already exposes this model: keep it as-is.
			continue
		}
		ownModels[id] = raw
	}
	d.setPayloadField("models", ownModels)

	d.mergeSorts(other)

	// Adopt payload fields this response does not define yet, such as a default
	// agent model or upstream additions, without overriding the base values.
	for key, raw := range other.payload {
		if _, ok := d.payload[key]; ok {
			continue
		}
		d.payload[key] = raw
	}
}

// mergeSorts appends model ordering contributed by other to the base ordering.
func (d *modelListDocument) mergeSorts(other *modelListDocument) {
	sourceEntries := other.sorts()
	if len(sourceEntries) == 0 {
		return
	}

	baseEntries := d.sorts()
	if len(baseEntries) == 0 {
		d.payload["agentModelSorts"] = other.payload["agentModelSorts"]
		return
	}

	changed := false
	for _, source := range sourceEntries {
		target := findSortEntry(baseEntries, source.displayName)
		if target == nil {
			baseEntries = append(baseEntries, source)
			changed = true
			continue
		}

		known := make(map[string]bool)
		for _, id := range target.modelIDs() {
			known[id] = true
		}
		for _, group := range source.groups {
			var fresh []string
			for _, id := range groupModelIDs(group) {
				if id == "" || known[id] {
					continue
				}
				known[id] = true
				fresh = append(fresh, id)
			}
			if len(fresh) > 0 {
				target.appendGroup(fresh)
				changed = true
			}
		}
	}

	if changed {
		d.setSorts(baseEntries)
	}
}

// ensureAllModelsSorted appends model IDs that no sort group references yet, so a
// picker rendering strictly from agentModelSorts still exposes every merged model.
func (d *modelListDocument) ensureAllModelsSorted() {
	entries := d.sorts()
	if len(entries) == 0 {
		return
	}
	models := d.models()
	if len(models) == 0 {
		return
	}

	referenced := make(map[string]bool, len(models))
	for _, entry := range entries {
		for _, id := range entry.modelIDs() {
			referenced[id] = true
		}
	}

	var leftovers []string
	for id, raw := range models {
		if referenced[id] || isDisabledModelEntry(raw) {
			continue
		}
		leftovers = append(leftovers, id)
	}
	if len(leftovers) == 0 {
		return
	}

	sort.Strings(leftovers)
	entries[len(entries)-1].appendGroup(leftovers)
	d.setSorts(entries)
}

// sorts decodes the agentModelSorts ordering, or nil when absent.
func (d *modelListDocument) sorts() []*modelSortEntry {
	if d == nil || d.payload == nil {
		return nil
	}
	raw, ok := d.payload["agentModelSorts"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var decoded []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	entries := make([]*modelSortEntry, 0, len(decoded))
	for _, item := range decoded {
		entry := &modelSortEntry{raw: item}
		if dn, ok := item["displayName"]; ok {
			_ = json.Unmarshal(dn, &entry.displayName)
		}
		if groups, ok := item["groups"]; ok {
			_ = json.Unmarshal(groups, &entry.groups)
		}
		entries = append(entries, entry)
	}
	return entries
}

// setSorts writes the ordering back into the payload, leaving untouched entries raw.
func (d *modelListDocument) setSorts(entries []*modelSortEntry) {
	encoded := make([]map[string]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		item := entry.raw
		if item == nil {
			item = make(map[string]json.RawMessage, 2)
		}
		if entry.dirty {
			groups, err := json.Marshal(entry.groups)
			if err != nil {
				continue
			}
			item["groups"] = groups
		}
		encoded = append(encoded, item)
	}
	d.setPayloadField("agentModelSorts", encoded)
}

// setPayloadField marshals value into a payload field.
func (d *modelListDocument) setPayloadField(name string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	d.payload[name] = raw
}

// marshal renders the document back to JSON, restoring the envelope it used on input.
func (d *modelListDocument) marshal() ([]byte, error) {
	if d.wrapped {
		nested, err := json.Marshal(d.payload)
		if err != nil {
			return nil, err
		}
		d.top["response"] = nested
	}
	return json.Marshal(d.top)
}

// modelIDs lists every model ID referenced by this ordering entry, in order.
func (e *modelSortEntry) modelIDs() []string {
	var ids []string
	for _, group := range e.groups {
		ids = append(ids, groupModelIDs(group)...)
	}
	return ids
}

// appendGroup adds a fresh ordering group holding the given model IDs.
func (e *modelSortEntry) appendGroup(ids []string) {
	if len(ids) == 0 {
		return
	}
	raw, err := json.Marshal(struct {
		ModelIDs []string `json:"modelIds"`
	}{ModelIDs: ids})
	if err != nil {
		return
	}
	e.groups = append(e.groups, raw)
	e.dirty = true
}

// groupModelIDs extracts the modelIds of one ordering group.
func groupModelIDs(raw json.RawMessage) []string {
	var group struct {
		ModelIDs []string `json:"modelIds"`
	}
	if err := json.Unmarshal(raw, &group); err != nil {
		return nil
	}
	return group.ModelIDs
}

// findSortEntry returns the ordering entry with the given display name.
func findSortEntry(entries []*modelSortEntry, displayName string) *modelSortEntry {
	if displayName == "" {
		return nil
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.displayName, displayName) {
			return entry
		}
	}
	return nil
}

// isDisabledModelEntry reports whether a raw model entry carries "disabled": true.
func isDisabledModelEntry(raw json.RawMessage) bool {
	var entry struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return false
	}
	return entry.Disabled
}
