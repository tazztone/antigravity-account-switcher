package proxy

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
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
		existing, ok := ownModels[id]
		if ok && !isDisabledModelEntry(existing) {
			// The base already exposes this model enabled: keep it as-is.
			continue
		}
		// If both are disabled, do not overwrite base's metadata.
		if ok && isDisabledModelEntry(existing) && isDisabledModelEntry(raw) {
			continue
		}
		ownModels[id] = raw
	}
	d.setPayloadField("models", ownModels)

	d.mergeSorts(other, ownModels)

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
// Models that are disabled or internal endpoints are omitted from agentModelSorts.
func (d *modelListDocument) mergeSorts(other *modelListDocument, ownModels map[string]json.RawMessage) {
	sourceEntries := other.sorts()
	if len(sourceEntries) == 0 {
		return
	}

	baseEntries := d.sorts()
	if len(baseEntries) == 0 {
		filtered := filterSortEntries(sourceEntries, ownModels)
		if len(filtered) > 0 {
			d.setSorts(filtered)
		}
		return
	}

	changed := false
	for _, source := range sourceEntries {
		target := findSortEntry(baseEntries, source.displayName)
		if target == nil {
			cleaned := filterSingleSortEntry(source, ownModels, nil)
			if len(cleaned.groups) > 0 {
				baseEntries = append(baseEntries, cleaned)
				changed = true
			}
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
				if isExcludedFromPicker(id, ownModels) {
					continue
				}
				known[id] = true
				fresh = append(fresh, id)
			}
			if len(fresh) > 0 {
				target.appendModels(fresh)
				changed = true
			}
		}
	}

	if changed {
		d.setSorts(baseEntries)
	}
}

// appendModels appends model IDs to the primary ordering group (groups[0]),
// or creates the primary group if none exists yet. This ensures IDE parity,
// as Antigravity IDE's model picker only renders groups[0].options.
func (e *modelSortEntry) appendModels(ids []string) {
	if len(ids) == 0 {
		return
	}
	if len(e.groups) == 0 {
		e.appendGroupWithRaw(nil, ids)
		return
	}

	var primary map[string]json.RawMessage
	if err := json.Unmarshal(e.groups[0], &primary); err == nil && primary != nil {
		var existingIDs []string
		if rawIDs, ok := primary["modelIds"]; ok {
			_ = json.Unmarshal(rawIDs, &existingIDs)
		}
		seen := make(map[string]bool, len(existingIDs)+len(ids))
		for _, id := range existingIDs {
			seen[id] = true
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				existingIDs = append(existingIDs, id)
			}
		}
		newIDsBytes, err := json.Marshal(existingIDs)
		if err == nil {
			primary["modelIds"] = newIDsBytes
			if marshaled, err := json.Marshal(primary); err == nil {
				e.groups[0] = marshaled
				e.dirty = true
				return
			}
		}
	}

	e.appendGroupWithRaw(nil, ids)
}

// appendGroup adds a fresh ordering group holding the given model IDs.
func (e *modelSortEntry) appendGroup(ids []string) {
	e.appendGroupWithRaw(nil, ids)
}

// appendGroupWithRaw appends an ordering group, preserving unknown group fields from originalRaw.
func (e *modelSortEntry) appendGroupWithRaw(originalRaw json.RawMessage, ids []string) {
	if len(ids) == 0 {
		return
	}
	if len(originalRaw) > 0 {
		var groupObj map[string]json.RawMessage
		if err := json.Unmarshal(originalRaw, &groupObj); err == nil && groupObj != nil {
			idBytes, err := json.Marshal(ids)
			if err == nil {
				groupObj["modelIds"] = idBytes
				if marshaled, err := json.Marshal(groupObj); err == nil {
					e.groups = append(e.groups, marshaled)
					e.dirty = true
					return
				}
			}
		}
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

// isExcludedFromPicker reports whether mid is excluded from the user-facing model picker.
func isExcludedFromPicker(mid string, models map[string]json.RawMessage) bool {
	if mid == "" {
		return true
	}
	lower := strings.ToLower(mid)
	if strings.Contains(lower, "disabled") || domain.IsInternalOrPreviewModel(mid) {
		return true
	}
	if raw, ok := models[mid]; ok && isDisabledModelEntry(raw) {
		return true
	}
	return false
}

// filterSortEntries filters out disabled and internal models from all sort entries.
func filterSortEntries(entries []*modelSortEntry, models map[string]json.RawMessage) []*modelSortEntry {
	var result []*modelSortEntry
	for _, entry := range entries {
		filtered := filterSingleSortEntry(entry, models, nil)
		if len(filtered.groups) > 0 {
			result = append(result, filtered)
		}
	}
	return result
}

// filterSingleSortEntry filters out disabled/internal model IDs from a sort entry's groups.
func filterSingleSortEntry(entry *modelSortEntry, models map[string]json.RawMessage, known map[string]bool) *modelSortEntry {
	if entry == nil {
		return nil
	}
	cleaned := &modelSortEntry{
		raw:         entry.raw,
		displayName: entry.displayName,
	}
	for _, grp := range entry.groups {
		var valid []string
		for _, id := range groupModelIDs(grp) {
			if id == "" || isExcludedFromPicker(id, models) {
				continue
			}
			if known != nil && known[id] {
				continue
			}
			if known != nil {
				known[id] = true
			}
			valid = append(valid, id)
		}
		if len(valid) > 0 {
			cleaned.appendGroupWithRaw(grp, valid)
		}
	}
	return cleaned
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
