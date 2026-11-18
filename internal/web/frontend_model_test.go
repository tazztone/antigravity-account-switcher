package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontendAppJS_ModelTierGroupingAndFiltering(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping frontend app.js unit test")
	}

	appJSBytes, err := embeddedDistFS.ReadFile("dist/app.js")
	if err != nil {
		t.Fatalf("failed to read embedded dist/app.js: %v", err)
	}

	harness := `
const assert = require('assert');
const fs = require('fs');
const vm = require('vm');

const appCode = fs.readFileSync(process.argv[2], 'utf8');
const hooks = {};
const noop = () => {};
const fakeElement = () => ({
  classList: { add: noop, remove: noop, toggle: noop },
  setAttribute: noop,
  addEventListener: noop,
  querySelector: () => null,
  querySelectorAll: () => [],
  appendChild: noop,
  style: {},
  innerHTML: '',
});

const sandbox = {
  console,
  Math,
  Date,
  Map,
  Set,
  Array,
  Object,
  String,
  Number,
  Boolean,
  RegExp,
  JSON,
  Promise,
  setTimeout,
  clearTimeout,
  setInterval: () => 0,
  clearInterval: noop,
  requestAnimationFrame: noop,
  localStorage: { getItem: () => 'false', setItem: noop },
  fetch: async () => ({ ok: false, status: 500, json: async () => ({}) }),
  window: { addEventListener: noop, location: { origin: 'http://127.0.0.1:8080' } },
  document: {
    body: { classList: { add: noop, remove: noop, toggle: noop } },
    getElementById: () => null,
    querySelector: () => null,
    querySelectorAll: () => [],
    addEventListener: noop,
    createElement: () => fakeElement(),
    createElementNS: () => fakeElement(),
  },
  __antigravityTestHooks: hooks,
};
sandbox.globalThis = sandbox;

vm.runInNewContext(appCode, sandbox, { filename: 'dist/app.js' });

// 1. parseModelTierInfo checks
const tierCases = [
  [{ model_id: 'gemini-4-hydrogen-high', display_name: 'Gemini 4 Hydrogen (High)' }, 'Gemini 4 Hydrogen', 'High'],
  [{ model_id: 'gemini-4-hydrogen-high', display_name: 'Gemini 4 Hydrogen (High) (gemini-4-hydrogen-high)' }, 'Gemini 4 Hydrogen', 'High'],
  [{ model_id: 'gemini-4-hydrogen-xhigh', display_name: 'Gemini 4 Hydrogen (gemini-4-hydrogen-xhigh)' }, 'Gemini 4 Hydrogen', 'XHigh'],
  [{ model_id: 'gemini-4-hydrogen-high-tiered', display_name: 'Gemini 4 Hydrogen (High Tiered)' }, 'Gemini 4 Hydrogen', 'High Tiered'],
  [{ model_id: 'gemini-4-hydrogen-xhigh-tiered', display_name: 'Gemini 4 Hydrogen (XHigh Tiered)' }, 'Gemini 4 Hydrogen', 'XHigh Tiered'],
  [{ model_id: 'gemini-4-hydrogen-high-tiered', display_name: 'Gemini 4 Hydrogen (High) (High Tiered)' }, 'Gemini 4 Hydrogen', 'High Tiered'],
  [{ model_id: 'gemini-3.8-flash-med', display_name: 'Gemini 3.8 Flash (gemini-3.8-flash-med)' }, 'Gemini 3.8 Flash', 'Med'],
  [{ model_id: 'gemini-3.8-flash-tiered', display_name: 'gemini-3.8-flash-tiered' }, 'Gemini 3.8 Flash', 'Tiered'],
  [{ model_id: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6 (Thinking)' }, 'Claude Sonnet 4.6', 'Think'],
  [{ model_id: 'orchestrator-v1', display_name: 'Orchestrator (orchestrator-v1)' }, 'Orchestrator', 'Std'],
];

for (const [rule, wantBase, wantTier] of tierCases) {
  const got = hooks.parseModelTierInfo(rule);
  assert.strictEqual(got.baseName, wantBase, 'baseName mismatch for ' + JSON.stringify(rule));
  assert.strictEqual(got.tier, wantTier, 'tier mismatch for ' + JSON.stringify(rule));
}

// 2. getVisibleAccountModelRules filtering
const filtered = hooks.getVisibleAccountModelRules({
  model_rules: [
    { model_id: 'live-model', discovered: true },
    { model_id: 'stale-model', discovered: false },
  ],
});
assert.deepStrictEqual(filtered.map(r => r.model_id), ['live-model']);

const legacyFailOpen = hooks.getVisibleAccountModelRules({
  model_rules: [
    { model_id: 'manual-1' },
    { model_id: 'manual-2', discovered: false },
  ],
});
assert.deepStrictEqual(legacyFailOpen.map(r => r.model_id), ['manual-1', 'manual-2']);

// 3. Account card grouping, badge ordering, Std suppression, and stale exclusion
const paidAcc = {
  id: 'acc-paid-1234',
  email: 'paid@example.com',
  status: 'active',
  is_active: true,
  active_categories: ['claude_gpt', 'gemini_argon'],
  disabled_categories: [],
  buckets: [],
  model_rules: [
    { model_id: 'gemini-4-hydrogen-low', display_name: 'Gemini 4 Hydrogen (Low)', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-4-hydrogen-med', display_name: 'Gemini 4 Hydrogen (Medium)', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-4-hydrogen', display_name: 'Gemini 4 Hydrogen (gemini-4-hydrogen)', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-4-hydrogen-high', display_name: 'Gemini 4 Hydrogen (High) (gemini-4-hydrogen-high)', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-4-hydrogen-tiered-high', display_name: 'Gemini 4 Hydrogen (High Tiered)', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-4-hydrogen-xhigh', display_name: 'Gemini 4 Hydrogen (gemini-4-hydrogen-xhigh)', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-4-hydrogen-tiered-xhigh', display_name: 'Gemini 4 Hydrogen (XHigh Tiered)', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-orchestrator', display_name: 'Orchestrator (gemini-orchestrator)', category: 'gemini', enabled: true, discovered: true },
  ],
};
const freeAcc = {
  id: 'acc-free-5678',
  email: 'free@example.com',
  status: 'active',
  is_active: false,
  active_categories: ['gemini'],
  disabled_categories: [],
  buckets: [],
  model_rules: [
    { model_id: 'gemini-4-hydrogen-sonar', display_name: 'Gemini 4 Hydrogen Sonar', category: 'gemini', enabled: true, discovered: true },
    { model_id: 'gemini-4-hydrogen', display_name: 'Gemini 4 Hydrogen', category: 'gemini', enabled: true, discovered: false },
  ],
};

hooks.setAccountsData([paidAcc, freeAcc]);
hooks.setAppConfig({ model_primary: 'gemini-2.5-pro', model_secondary: 'gemini-2.5-flash', fallback_secondary_enabled: false });

const exclusiveMap = hooks.computeExclusiveModelMap();
const hydrogenProviders = exclusiveMap.baseNameProviders.get('gemini 4 hydrogen');
assert.ok(hydrogenProviders && hydrogenProviders.size === 1 && hydrogenProviders.has('acc-paid-1234'),
  'stale discovered=false rule on freeAcc must not block Pool Route exclusivity for paidAcc');

const paidCard = hooks.createAccountCard(paidAcc, exclusiveMap);
const rowMatches = paidCard.innerHTML.match(/class="model-rule-row"/g) || [];
assert.strictEqual(rowMatches.length, 2, 'expected 2 collapsed model rows (Gemini 4 Hydrogen + Orchestrator), got ' + rowMatches.length);
assert.ok(paidCard.innerHTML.includes('Models (2)'), 'expected Gemini header to count Models (2)');
const ruleNames = [...paidCard.innerHTML.matchAll(/class="model-rule-name">([^<]+)<\/span>/g)].map(m => m[1].trim());
assert.deepStrictEqual(ruleNames, ['Gemini 4 Hydrogen', 'Orchestrator']);

// Verify badge ordering on Gemini 4 Hydrogen row
const hydrogenSection = paidCard.innerHTML.split('Orchestrator')[0];
const badgeMatches = [...hydrogenSection.matchAll(/class="model-tier-badge"[^>]*>\s*([^<\n]+?)\s*<\/span>/g)].map(m => m[1].trim());
assert.deepStrictEqual(badgeMatches, ['Low', 'Med', 'Std', 'High', 'High Tiered', 'XHigh', 'XHigh Tiered']);
assert.ok(hydrogenSection.includes('Pool Route ⚡'), 'expected Pool Route badge on Gemini 4 Hydrogen');

// Verify solo Orchestrator suppresses spurious Std badge
const orchestratorSection = paidCard.innerHTML.split('Orchestrator')[1] || '';
assert.ok(!orchestratorSection.includes('model-tier-badge'), 'solo Orchestrator must not render a spurious Std badge');

// Verify stale discovered=false model is hidden on freeAcc card
const freeCard = hooks.createAccountCard(freeAcc, exclusiveMap);
assert.ok(freeCard.innerHTML.includes('Gemini 4 Hydrogen Sonar'), 'expected live model on free card');
assert.ok(freeCard.innerHTML.includes('Models (1)'), 'expected stale discovered=false model hidden from Models count');
`

	tmpDir := t.TempDir()
	appPath := filepath.Join(tmpDir, "app.js")
	testPath := filepath.Join(tmpDir, "run_test.js")

	if err := os.WriteFile(appPath, appJSBytes, 0o600); err != nil {
		t.Fatalf("write temp app.js: %v", err)
	}
	if err := os.WriteFile(testPath, []byte(harness), 0o600); err != nil {
		t.Fatalf("write temp test script: %v", err)
	}

	cmd := exec.Command(nodeBin, testPath, appPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node frontend test failed: %v\noutput:\n%s", err, strings.TrimSpace(string(out)))
	}
}
