# Suggestions & Backlog

## What's Next
- Visually verify rendered CSS Subgrid alignment, `Compact Models` toggle, and Global 429 Fallback Bar across desktop (`>= 1280px`), tablet (`~900px`), and mobile (`~390px`) viewports in a live browser session.
- Consider surfacing `GET /api/conversations` ([api.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/web/api.go#L650)) in a conversation summary table or drawer if per-thread token breakdowns are needed beyond log chips.

## Issues & Room for Improvement
- Stale SQLite `account_model_rules.display_name` rows cached prior to `extractModelVariant` ([language_server.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/quota/language_server.go#L418-L444)) remain until `/api/models` or `UpsertAccountModels` refreshes them (frontend `parseModelTierInfo` already renders them as `XHigh`).
- `drawPoolCircuits()` ([app.js](file:///home/tazztone/_coding/antigravity-account-switcher/internal/web/dist/app.js#L513)) draws SVG curves across `.quota-group` cards; with 3+ accounts wrapping onto a second grid row, routing lines along gutters instead of direct point-to-point Beziers would further reduce visual clutter.

## Reusable Takeaways
- Always check `xhigh` before `high` (and `minimal` before `medium`/`low`) in substring variant parsers ([language_server.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/quota/language_server.go#L418-L444)).
- `POST /api/config` ([api.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/web/api.go#L991-L1003)) ignores empty `model_primary`/`model_secondary` strings; disable 429 fallback exclusively via `fallback_secondary_enabled: false`.
- Keep global utility classes (`.hidden`, `.btn-sm`) explicitly defined in `style.css` rather than relying on scoped selectors like `.btn-jump-latest.hidden`.
