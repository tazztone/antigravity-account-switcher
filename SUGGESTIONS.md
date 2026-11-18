# Suggestions & Backlog

## What's Next
- Live-verify `./scripts/reinstall.sh --restart` / `./scripts/restart.sh` while both `codium2` and `agy2` are open on port `1831`, confirming both clients resume routing without restarting.
- Visually verify rendered CSS Subgrid alignment, `Compact Models` toggle, and Global 429 Fallback Bar across desktop (`>= 1280px`), tablet (`~900px`), and mobile (`~390px`) viewports in a live browser session.
- Consider surfacing `GET /api/conversations` ([api.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/web/api.go#L650)) in a conversation summary table or drawer if per-thread token breakdowns are needed beyond log chips.

## Issues & Room for Improvement
- `executeStop` ([main.go](file:///home/tazztone/_coding/antigravity-account-switcher/cmd/antigravity-account-switcher/main.go#L396-L401)): `config.Load()` initializes `DefaultConfig()` (`Port: 8080`), making the fallback `candidatePorts = []int{config.DefaultPort, 1831}` branch unreachable when `config.json` omits `"port"`. Running `stop` without `--port` misses a `serve --port 1831` daemon unless `port: 1831` is saved in `config.json`.
- `FindLocalLanguageServer` ([language_server.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/quota/language_server.go#L95-L103)): returns the first `/proc/<pid>` matching `language_server` + `--csrf_token`, so concurrent `codium2` and `agy2` sessions only probe one local language server via `/proc` (Cloud Code PA HTTP polling still covers all accounts).
- `drawPoolCircuits()` ([app.js](file:///home/tazztone/_coding/antigravity-account-switcher/internal/web/dist/app.js#L513)) draws SVG curves across `.quota-group` cards; with 3+ accounts wrapping onto a second grid row, routing lines along gutters instead of direct point-to-point Beziers would further reduce visual clutter.

## Reusable Takeaways
- In bash scripts parsing `ps -eo pid=,args=`, use `while read -r pid args` (default `IFS`) instead of `while IFS= read -r line` + `${line%% *}`, because `ps` right-aligns PIDs with leading spaces.
- Daemon lifecycle scripts must target only `serve` subcommands (never `launch` or `wrap`, whose `PR_SET_PDEATHSIG` kills supervised editor/CLI sessions).
- Always check `xhigh` before `high` (and `minimal` before `medium`/`low`) in substring variant parsers ([language_server.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/quota/language_server.go#L418-L444)).
- `POST /api/config` ([api.go](file:///home/tazztone/_coding/antigravity-account-switcher/internal/web/api.go#L991-L1003)) ignores empty `model_primary`/`model_secondary` strings; disable 429 fallback exclusively via `fallback_secondary_enabled: false`.
- Keep global utility classes (`.hidden`, `.btn-sm`) explicitly defined in `style.css` rather than relying on scoped selectors like `.btn-jump-latest.hidden`.
