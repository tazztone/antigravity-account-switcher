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

const fakeElementsById = {};
const getOrCreateById = (id) => {
  if (!fakeElementsById[id]) {
    fakeElementsById[id] = fakeElement();
  }
  return fakeElementsById[id];
};

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
    getElementById: (id) => getOrCreateById(id),
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
  active_models: [],
  active_categories: ['claude_gpt', 'gemini'],
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
  active_categories: [],
  active_models: ['gemini-4-hydrogen-sonar'],
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

// Verify Orchestrator (Argon), (Carbon), (Neon) stay distinct rows with tier === ''
const orchArgon = hooks.parseModelTierInfo({ model_id: 'gemini-pro-orchestrator-argon', display_name: 'Orchestrator (Argon)' });
const orchCarbon = hooks.parseModelTierInfo({ model_id: 'gemini-pro-orchestrator', display_name: 'Orchestrator (Carbon)' });
const orchNeon = hooks.parseModelTierInfo({ model_id: 'gemini-pro-orchestrator-neon', display_name: 'Orchestrator (Neon)' });
assert.strictEqual(orchArgon.baseName, 'Orchestrator (Argon)');
assert.strictEqual(orchArgon.tier, '');
assert.strictEqual(orchCarbon.baseName, 'Orchestrator (Carbon)');
assert.strictEqual(orchCarbon.tier, '');
assert.strictEqual(orchNeon.baseName, 'Orchestrator (Neon)');
assert.strictEqual(orchNeon.tier, '');

// Verify 2 categories on paidCard (ROUTING (2/2), no Gemini 4 Argon card)
assert.ok(paidCard.innerHTML.includes('ROUTING (2/2)'), 'expected ROUTING (2/2) badge on paidCard');
assert.ok(!paidCard.innerHTML.includes('data-category-group="gemini_argon"'), 'expected no gemini_argon category group');

// Verify gemini-4-argon-high renders inside Gemini Models group (not a separate card)
const argonAcc = {
  ...paidAcc,
  model_rules: [
    ...paidAcc.model_rules,
    { model_id: 'gemini-4-argon-high', display_name: 'Gemini 4 Argon (High)', category: 'gemini', enabled: true, discovered: true },
  ],
};
const argonCard = hooks.createAccountCard(argonAcc, exclusiveMap);
assert.ok(!argonCard.innerHTML.includes('data-category-group="gemini_argon"'), 'expected no separate gemini_argon card');
const geminiGroupHtml = argonCard.innerHTML.split('data-category-group="gemini"')[1] || '';
assert.ok(geminiGroupHtml.includes('Gemini 4 Argon'), 'expected Gemini 4 Argon row inside Gemini Models group');

// Verify stale discovered=false model is hidden on freeAcc card and model priority pin renders
const freeCard = hooks.createAccountCard(freeAcc, exclusiveMap);
assert.ok(freeCard.innerHTML.includes('Gemini 4 Hydrogen Sonar'), 'expected live model on free card');
assert.ok(freeCard.innerHTML.includes('Models (1)'), 'expected stale discovered=false model hidden from Models count');
assert.ok(freeCard.innerHTML.includes('model-rule-row is-model-pinned'), 'expected is-model-pinned row class on pinned model');
assert.ok(freeCard.innerHTML.includes('model-pin-btn is-pinned'), 'expected is-pinned button class on pinned model');
assert.ok(freeCard.innerHTML.includes('● Priority'), 'expected active Priority pill label');
assert.ok(freeCard.innerHTML.includes('ROUTING (1 MODEL)'), 'expected ROUTING (1 MODEL) header badge when only model pin is active');
assert.ok(paidCard.innerHTML.includes('AI Pro · $20/mo'), 'expected account plan badge in card header');

// 4. Plan Value formatting helpers
assert.strictEqual(hooks.formatUSD(12.345), '$12.35');
assert.strictEqual(hooks.formatUSD(0.0042), '$0.0042');
assert.strictEqual(hooks.formatUSD(null), '--');
assert.strictEqual(hooks.formatMultiplier(5.26), '5.3x');
assert.strictEqual(hooks.formatPlanBadge('ai_ultra', 250), 'AI Ultra · $250/mo');
assert.strictEqual(hooks.formatPlanBadge('free', 0), 'Free · $0/mo');
assert.ok(hooks.formatConfidenceBadge('high').includes('conf-high'));

// 5. renderPlanValueReport: benchmark Edit action, Legacy badge on unknown model, cohort spread, legacy hero note
hooks.renderPlanValueReport({
  pool_summary: {
    realized_api_usd: 120.5,
    total_requests: 500,
    total_tokens: 1500000,
    legacy_unpriced_requests: 42,
    avg_claude_5h_cap_usd: 12.0,
    avg_claude_weekly_cap_usd: 50.0,
    min_claude_weekly_cap_usd: 40.0,
    max_claude_weekly_cap_usd: 60.0,
    claude_weekly_confidence: 'medium',
    claude_observed_delta: 0.25,
    cache_hit_rate: 0.9,
  },
  benchmarks: [
    { id: 'agy-ai-pro-claude', plan_name: 'AGY AI Pro (Claude/GPT pool)', monthly_cost_usd: 20, window_5h_api_usd: 12, weekly_api_usd: 50, est_monthly_api_usd: 214.29, value_per_dollar: 10.7, is_measured: true, confidence: 'medium' },
    { id: 'claude-pro', provider: 'Anthropic', plan_name: 'Claude Pro', monthly_cost_usd: 20, window_5h_api_usd: 10, weekly_api_usd: 50, est_monthly_api_usd: 214.29, value_per_dollar: 10.7, is_measured: false },
  ],
  by_account: [
    { email: 'a@example.com', plan_tier: 'ai_pro', monthly_cost_usd: 20, realized_api_usd: 80.0, realized_multiplier: 4.0, cache_hit_rate: 0.9, calibrations: [] },
  ],
  by_model: [
    { model: 'gemini-4-hydrogen', requests: 100, uncached_input_tokens: 1000, cached_input_tokens: 9000, output_tokens: 500, thoughts_tokens: 0, cache_hit_rate: 0.9, api_value_usd: 10.0, priced: true, pricing_pattern: 'gemini-4-hydrogen' },
    { model: 'unknown', requests: 42, uncached_input_tokens: 500, cached_input_tokens: 0, output_tokens: 100, thoughts_tokens: 0, cache_hit_rate: 0.0, api_value_usd: 0, priced: false },
  ],
  pricing_catalog: [],
});

const benchHTML = getOrCreateById('value-benchmarks-body').innerHTML;
assert.ok(benchHTML.includes('btn-edit-benchmark" data-id="claude-pro"'), 'expected Edit button on competitor row');
assert.ok(!benchHTML.includes('data-id="agy-ai-pro-claude"'), 'measured AGY row must not render Edit button');

const modelsHTML = getOrCreateById('value-models-body').innerHTML;
assert.ok(modelsHTML.includes('Legacy</span>'), 'unknown model row should render Legacy badge');
assert.ok(!modelsHTML.includes('data-pattern="unknown"'), 'unknown model row must not render Set Price button');

const claudeSubText = getOrCreateById('val-claude-sub').textContent;
assert.ok(claudeSubText.includes('Seats $40.00'), 'expected cohort spread in #val-claude-sub, got: ' + claudeSubText);
const realizedSubText = getOrCreateById('val-realized-sub').textContent;
assert.ok(realizedSubText.includes('42 legacy reqs excl.'), 'expected legacy count note in #val-realized-sub, got: ' + realizedSubText);
const accHTML = getOrCreateById('value-accounts-body').innerHTML;
assert.ok(accHTML.includes('(4.0x)'), 'expected realized multiplier in per-account table');
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
