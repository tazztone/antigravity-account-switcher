// Antigravity Account Switcher - Production Dashboard Application
// Server-driven • Offline-first • Tabular-nums • Zero-clipping Tooltip
(function () {
  'use strict';

  // Application State
  let currentPeriod = 'lifetime';
  let eventSource = null;
  let rawTimelineData = [];
  let rawAccountsData = [];
  let rawModelsData = [];
  let rawCategoryModes = { claude_gpt: 'pool', gemini: 'pool', gemini_argon: 'pool' };
  let currentAppConfig = {
    model_primary: 'gemini-2.5-pro',
    model_secondary: 'gemini-2.5-flash',
    fallback_secondary_enabled: false
  };
  let allEvents = [];
  let currentLogFilter = 'all';
  let isUserScrolledUp = false;
  let selectedColumnIndex = -1;

  // Privacy Mode State (Persistent in localStorage)
  const PRIVACY_STORAGE_KEY = 'antigravity_privacy_mode';
  let isPrivacyMode = localStorage.getItem(PRIVACY_STORAGE_KEY) === 'true';
  const EMAIL_REGEX = /[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}/g;

  // DOM Elements
  const logsContainer = document.getElementById('logs-container');
  const accountsGrid = document.getElementById('accounts-grid');
  const toastContainer = document.getElementById('toast-container');
  const chartCard = document.getElementById('timeline-chart-card');
  const chartViewport = document.getElementById('chart-viewport');
  const barsTrack = document.getElementById('bars-track');
  const chartInspector = document.getElementById('chart-inspector');
  const inspectorDate = document.getElementById('inspector-date');
  const inspectorData = document.getElementById('inspector-data');
  const chartTooltip = document.getElementById('chart-tooltip');
  const chartXAxis = document.getElementById('chart-x-axis');
  const gridMaxLabel = document.getElementById('grid-max-label');
  const gridMidLabel = document.getElementById('grid-mid-label');

  // Discovery Controls DOM Elements
  const btnRefreshModels = document.getElementById('btn-refresh-models');
  const refreshModelsIcon = document.getElementById('refresh-models-icon');
  const modelsSourceBadge = document.getElementById('models-source-badge');

  // =========================================================================
  // Formatting & Utility Helpers
  // =========================================================================

  function escapeHtml(str) {
    if (str === null || str === undefined) return '';
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  function redactEmails(text) {
    if (!text) return '';
    return String(text).replace(EMAIL_REGEX, '[redacted@email.com]');
  }

  function formatLogMessage(text) {
    if (!text) return '';
    const escaped = escapeHtml(text);
    return escaped.replace(EMAIL_REGEX, match => `<span class="blurred-email">${match}</span>`);
  }

  function updatePrivacyUI() {
    const btn = document.getElementById('btn-privacy');
    const textEl = document.getElementById('privacy-btn-text');
    const iconPath = document.getElementById('privacy-icon-path');

    if (isPrivacyMode) {
      document.body.classList.add('privacy-mode');
      if (btn) {
        btn.classList.add('btn-privacy-active');
        btn.setAttribute('aria-pressed', 'true');
        btn.title = 'Privacy Mode Active (Press P to toggle)';
      }
      if (textEl) textEl.textContent = 'Privacy: ON';
      if (iconPath) {
        iconPath.setAttribute('d', 'M13.875 18.825A10.05 10.05 0 0112 19c-4.478 0-8.268-2.943-9.543-7a9.97 9.97 0 011.563-3.029m5.858.908a3 3 0 114.243 4.243M9.878 9.878l4.242 4.242M9.88 9.88l-3.29-3.29m7.532 7.532l3.29 3.29M3 3l18 18');
      }
    } else {
      document.body.classList.remove('privacy-mode');
      if (btn) {
        btn.classList.remove('btn-privacy-active');
        btn.setAttribute('aria-pressed', 'false');
        btn.title = 'Toggle Privacy Mode (Press P)';
      }
      if (textEl) textEl.textContent = 'Privacy';
      if (iconPath) {
        iconPath.setAttribute('d', 'M15 12a3 3 0 11-6 0 3 3 0 016 0z M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z');
      }
    }

    // Refresh active route email title tooltip
    const activeEmailEl = document.getElementById('active-email');
    if (activeEmailEl && activeEmailEl.getAttribute('data-real-email')) {
      const realEmail = activeEmailEl.getAttribute('data-real-email');
      activeEmailEl.title = isPrivacyMode ? '[Protected in Privacy Mode]' : `${realEmail} (Click to copy)`;
    }

    // Re-render accounts grid if accounts data loaded
    if (rawAccountsData && rawAccountsData.length > 0) {
      renderAccountsGrid();
    }

    // Reapply log filtering to refresh blurred spans
    reapplyLogFilter();
  }

  function togglePrivacyMode() {
    isPrivacyMode = !isPrivacyMode;
    try {
      localStorage.setItem(PRIVACY_STORAGE_KEY, isPrivacyMode ? 'true' : 'false');
    } catch (_) {}
    updatePrivacyUI();
    showToast(isPrivacyMode ? 'Privacy Mode enabled (emails blurred for screenshots)' : 'Privacy Mode disabled', 'info', 2500);
  }

  function formatNumber(num) {
    if (num === null || num === undefined || isNaN(num)) return '0';
    return Number(num).toLocaleString('en-US');
  }

  function formatCompactNumber(num) {
    if (!num || isNaN(num)) return '0';
    const n = Number(num);
    if (n >= 1e9) return (n / 1e9).toFixed(1).replace(/\.0$/, '') + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'K';
    return n.toLocaleString('en-US');
  }

  function formatTimeOnly(isoStr) {
    if (!isoStr) return '--:--:--';
    const d = new Date(isoStr);
    if (isNaN(d.getTime())) return '--:--:--';
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  }

  function formatDateLabel(isoDateStr) {
    if (!isoDateStr) return '--';
    const parts = isoDateStr.split('-');
    if (parts.length === 3) {
      const d = new Date(Number(parts[0]), Number(parts[1]) - 1, Number(parts[2]));
      return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
    }
    return isoDateStr;
  }

  function getLocalDateStr(d = new Date()) {
    const year = d.getFullYear();
    const month = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${year}-${month}-${day}`;
  }

  function getClientTimezoneInfo() {
    let tz = '';
    try {
      tz = Intl.DateTimeFormat().resolvedOptions().timeZone || '';
    } catch (_) {
      tz = '';
    }
    const offsetMinutes = -new Date().getTimezoneOffset();
    return { tz, offsetMinutes };
  }

  // Calculate live countdown timer from reset time string
  function getRemainingSeconds(resetTimeStr) {
    if (!resetTimeStr) return 0;
    const target = new Date(resetTimeStr).getTime();
    if (isNaN(target) || target <= 0) return 0;
    const diff = Math.floor((target - Date.now()) / 1000);
    return Math.max(0, diff);
  }

  function formatRemainingDuration(totalSeconds) {
    if (totalSeconds <= 0) return 'Reset ready';
    const d = Math.floor(totalSeconds / 86400);
    const h = Math.floor((totalSeconds % 86400) / 3600);
    const m = Math.floor((totalSeconds % 3600) / 60);
    const s = totalSeconds % 60;

    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m ${s}s`;
    if (m > 0) return `${m}m ${s}s`;
    return `${s}s`;
  }

  // =========================================================================
  // Non-Blocking Toast Notification System
  // =========================================================================

  function showToast(message, type = 'info', duration = 4000) {
    if (!toastContainer) return;

    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    toast.setAttribute('role', 'alert');

    toast.innerHTML = `
      <div class="toast-message">${escapeHtml(message)}</div>
      <button class="toast-close" aria-label="Dismiss notification">&times;</button>
    `;

    const closeBtn = toast.querySelector('.toast-close');
    const dismiss = () => {
      toast.style.opacity = '0';
      toast.style.transform = 'translateY(6px)';
      setTimeout(() => {
        if (toast.parentNode) toast.parentNode.removeChild(toast);
      }, 150);
    };

    closeBtn.addEventListener('click', dismiss);
    toastContainer.appendChild(toast);

    if (duration > 0) {
      setTimeout(dismiss, duration);
    }
  }

  async function copyToClipboard(text, label = 'Content') {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
        showToast(`${label} copied to clipboard`, 'success', 2500);
      } else {
        const textarea = document.createElement('textarea');
        textarea.value = text;
        textarea.style.position = 'fixed';
        textarea.style.opacity = '0';
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand('copy');
        document.body.removeChild(textarea);
        showToast(`${label} copied to clipboard`, 'success', 2500);
      }
    } catch (e) {
      showToast(`Failed to copy to clipboard: ${e.message}`, 'error', 3000);
    }
  }

  // =========================================================================
  // Status & Active Account Management
  // =========================================================================

  async function fetchStatus() {
    try {
      const res = await fetch('/api/status');
      if (!res.ok) return;
      const data = await res.json();

      // Version badge
      const versionEl = document.getElementById('switcher-version');
      if (versionEl && data.version) {
        versionEl.textContent = data.version.startsWith('v') ? data.version : `v${data.version}`;
      }

      // Active Account Route
      const activeEmailEl = document.getElementById('active-email');
      const activeDetailsEl = document.getElementById('active-details');
      const activeBadge = document.getElementById('active-status-badge');
      const copyActiveBtn = document.getElementById('btn-copy-active-email');

      if (data.active_account) {
        const email = data.active_account.email;
        activeEmailEl.textContent = email;
        activeEmailEl.setAttribute('data-real-email', email);
        activeEmailEl.title = isPrivacyMode ? '[Protected in Privacy Mode]' : `${email} (Click to copy)`;
        activeEmailEl.style.cursor = isPrivacyMode ? 'default' : 'pointer';
        activeEmailEl.onclick = () => {
          const toCopy = isPrivacyMode ? '[redacted@email.com]' : email;
          copyToClipboard(toCopy, isPrivacyMode ? 'Redacted email' : 'Account email');
        };

        if (copyActiveBtn) {
          copyActiveBtn.style.display = 'inline-flex';
          copyActiveBtn.title = isPrivacyMode ? 'Copy redacted email' : 'Copy active email';
          copyActiveBtn.onclick = () => {
            const toCopy = isPrivacyMode ? '[redacted@email.com]' : email;
            copyToClipboard(toCopy, isPrivacyMode ? 'Redacted email' : 'Account email');
          };
        }

        let createdDateStr = 'recent';
        if (data.active_account.created_at) {
          const d = new Date(data.active_account.created_at);
          if (!isNaN(d.getTime())) {
            createdDateStr = d.toLocaleDateString();
          }
        }
        activeDetailsEl.textContent = `ID: ${data.active_account.id.slice(0, 8)}... • Added ${createdDateStr} • HTTP 429 auto-failover armed`;

        const isExhausted = data.active_account.status === 'exhausted';
        activeBadge.textContent = (data.active_account.status || 'ACTIVE').toUpperCase();
        activeBadge.className = isExhausted ? 'badge badge-danger' : 'badge badge-success';
      } else {
        activeEmailEl.removeAttribute('data-real-email');
        activeEmailEl.textContent = 'No Active Google Account';
        activeEmailEl.title = 'No account selected';
        activeEmailEl.style.cursor = 'default';
        activeEmailEl.onclick = null;
        if (copyActiveBtn) copyActiveBtn.style.display = 'none';

        activeDetailsEl.textContent = 'Authenticate an account or select one below to arm proxy';
        activeBadge.textContent = 'STANDBY';
        activeBadge.className = 'badge badge-warning';
      }

      // Quick Stats
      const accountsCountStat = document.getElementById('accounts-stat-count');
      const accountsSubtext = document.getElementById('accounts-stat-subtext');
      if (accountsCountStat) {
        accountsCountStat.textContent = data.total_accounts || 0;
      }
      if (accountsSubtext) {
        const standby = Math.max(0, (data.total_accounts || 0) - (data.active_account ? 1 : 0));
        accountsSubtext.textContent = standby > 0 ? `${standby} standby failover target${standby === 1 ? '' : 's'}` : 'Single account routing';
      }

      if (data.category_modes) {
        rawCategoryModes = Object.assign({}, rawCategoryModes, data.category_modes);
      }
      updateHeaderRoutingStrip();
    } catch (e) {
      console.warn('Failed to fetch status:', e);
    }
  }

  // =========================================================================
  // Accounts Pool & Live Quota Matrix
  // =========================================================================

  function updateHeaderRoutingStrip() {
    const stripEl = document.getElementById('header-routing-strip');
    if (stripEl) {
      stripEl.innerHTML = '';
      stripEl.style.display = 'none';
    }
  }

  async function fetchAccounts() {
    try {
      const res = await fetch('/api/accounts');
      if (!res.ok) return;
      const accounts = await res.json();
      rawAccountsData = Array.isArray(accounts) ? accounts : [];

      if (rawAccountsData.length > 0 && rawAccountsData[0].category_modes) {
        rawCategoryModes = Object.assign({}, rawCategoryModes, rawAccountsData[0].category_modes);
      }

      const countBadge = document.getElementById('accounts-count-badge');
      if (countBadge) {
        countBadge.textContent = `${rawAccountsData.length} account${rawAccountsData.length === 1 ? '' : 's'}`;
      }

      renderAccountsGrid();
      updateHeaderRoutingStrip();
    } catch (e) {
      console.warn('Failed to fetch accounts:', e);
    }
  }

  function renderAccountsGrid() {
    if (!accountsGrid) return;
    accountsGrid.innerHTML = '';

    if (rawAccountsData.length === 0) {
      accountsGrid.innerHTML = `
        <div class="empty-state" style="grid-column: 1 / -1;">
          <div class="empty-state-title">No Google Accounts in Pool</div>
          <div class="empty-state-subtext">
            Authenticate your first Google account with 1-Click OAuth to start proxying Google Antigravity 2.0 requests with zero-latency failover.
          </div>
          <button class="btn btn-primary btn-add-first-account" style="margin-top: 0.5rem;">
            Authenticate Account Now
          </button>
        </div>
      `;

      const addFirstBtn = accountsGrid.querySelector('.btn-add-first-account');
      if (addFirstBtn) {
        addFirstBtn.addEventListener('click', handleAddAccount);
      }
      return;
    }

    rawAccountsData.forEach(acc => {
      const card = createAccountCard(acc);
      accountsGrid.appendChild(card);
    });

    requestAnimationFrame(() => drawPoolCircuits());
  }

  function drawPoolCircuits() {
    if (!accountsGrid || rawAccountsData.length < 2) {
      const existing = document.getElementById('pool-circuit-canvas');
      if (existing) existing.remove();
      return;
    }

    let svg = document.getElementById('pool-circuit-canvas');
    if (!svg) {
      svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.id = 'pool-circuit-canvas';
      svg.setAttribute('class', 'pool-circuit-canvas');
      accountsGrid.appendChild(svg);
    }
    svg.innerHTML = '';

    const gridRect = accountsGrid.getBoundingClientRect();
    if (gridRect.width === 0 || gridRect.height === 0) return;

    svg.setAttribute('viewBox', `0 0 ${gridRect.width} ${gridRect.height}`);

    const categories = ['claude_gpt', 'gemini', 'gemini_argon'];

    categories.forEach(catKey => {
      const activeEl = accountsGrid.querySelector(`.quota-group.bucket-active-pool[data-category-group="${catKey}"]`);
      const standbyEls = Array.from(accountsGrid.querySelectorAll(`.quota-group.bucket-standby-pool[data-category-group="${catKey}"]`));

      if (!activeEl || standbyEls.length === 0) return;

      const sequence = [activeEl, ...standbyEls];

      for (let i = 0; i < sequence.length - 1; i++) {
        const fromEl = sequence[i];
        const toEl = sequence[i + 1];

        const r1 = fromEl.getBoundingClientRect();
        const r2 = toEl.getBoundingClientRect();

        const h1 = fromEl.querySelector('.quota-group-header');
        const h2 = toEl.querySelector('.quota-group-header');
        const r1H = h1 ? h1.getBoundingClientRect() : r1;
        const r2H = h2 ? h2.getBoundingClientRect() : r2;

        let x1, y1, x2, y2;

        if (r1.left < r2.left) {
          x1 = r1.right - gridRect.left;
          y1 = (r1H.top + r1H.bottom) / 2 - gridRect.top;
          x2 = r2.left - gridRect.left;
          y2 = (r2H.top + r2H.bottom) / 2 - gridRect.top;
        } else if (r1.left > r2.left) {
          x1 = r1.left - gridRect.left;
          y1 = (r1H.top + r1H.bottom) / 2 - gridRect.top;
          x2 = r2.right - gridRect.left;
          y2 = (r2H.top + r2H.bottom) / 2 - gridRect.top;
        } else {
          x1 = (r1.left + r1.right) / 2 - gridRect.left;
          y1 = r1.bottom - gridRect.top;
          x2 = (r2.left + r2.right) / 2 - gridRect.left;
          y2 = r2.top - gridRect.top;
        }

        const dx = Math.abs(x2 - x1) * 0.5;
        let d = '';

        if (Math.abs(r1.left - r2.left) < 10) {
          d = `M ${x1} ${y1} C ${x1} ${y1 + 15}, ${x2} ${y2 - 15}, ${x2} ${y2}`;
        } else {
          const cx1 = x1 + (x2 > x1 ? dx : -dx);
          const cx2 = x2 - (x2 > x1 ? dx : -dx);
          d = `M ${x1} ${y1} C ${cx1} ${y1}, ${cx2} ${y2}, ${x2} ${y2}`;
        }

        const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
        path.setAttribute('class', 'circuit-path');
        path.setAttribute('data-category', catKey);
        path.setAttribute('d', d);
        svg.appendChild(path);

        const dot1 = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
        dot1.setAttribute('class', 'circuit-dot origin-dot');
        dot1.setAttribute('data-category', catKey);
        dot1.setAttribute('cx', x1);
        dot1.setAttribute('cy', y1);
        dot1.setAttribute('r', '2.5');
        svg.appendChild(dot1);

        const dot2 = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
        dot2.setAttribute('class', 'circuit-dot target-dot');
        dot2.setAttribute('data-category', catKey);
        dot2.setAttribute('cx', x2);
        dot2.setAttribute('cy', y2);
        dot2.setAttribute('r', '2.5');
        svg.appendChild(dot2);
      }
    });
  }

  function createAccountCard(acc) {
    const card = document.createElement('div');
    const isActive = Boolean(acc.is_active);
    const isExhausted = acc.status === 'exhausted';

    card.className = `account-card ${isActive ? 'is-active' : ''}`;
    card.setAttribute('data-account-id', acc.id);

    const buckets = acc.buckets || [];

    function findBucket(windowPattern, namePattern, excludePattern) {
      return buckets.find(b => {
        const idOrName = ((b.bucket_id || '') + ' ' + (b.display_name || '')).toLowerCase();
        const windowMatch = (b.window || '').toLowerCase().includes(windowPattern.toLowerCase());
        const nameMatch = idOrName.includes(namePattern.toLowerCase()) &&
          !(excludePattern && idOrName.includes(excludePattern.toLowerCase()));
        return windowMatch && nameMatch;
      }) || null;
    }

    const claude5h = findBucket('5h', '3p') || findBucket('5h', 'claude');
    const claudeWeekly = findBucket('weekly', '3p') || findBucket('weekly', 'claude');
    const argonWeekly = findBucket('weekly', 'argon');
    const gemini5h = findBucket('5h', 'gemini', 'argon');
    const geminiWeekly = findBucket('weekly', 'gemini', 'argon');

    // Identify standard buckets to find any custom/other buckets
    const standardBucketSet = new Set([claude5h, claudeWeekly, gemini5h, geminiWeekly, argonWeekly].filter(Boolean));
    const extraBuckets = buckets.filter(b => !standardBucketSet.has(b));

    function renderQuotaRow(label, bucket) {
      if (!bucket) {
        return `
          <div class="quota-row">
            <div class="quota-labels">
              <span class="quota-name">${escapeHtml(label)}</span>
              <span class="quota-pct text-muted tabular-nums">--%</span>
            </div>
            <div class="quota-bar-track">
              <div class="quota-bar-fill bar-healthy" style="width: 100%; opacity: 0.15;"></div>
            </div>
          </div>
        `;
      }

      const fraction = Math.max(0, Math.min(1, bucket.remaining_fraction ?? 1.0));
      const pct = Math.round(fraction * 100);
      const isDepleted = pct === 0;

      let barClass = 'bar-healthy';
      let textClass = 'text-success';
      if (pct <= 20) {
        barClass = 'bar-danger';
        textClass = 'text-danger';
      } else if (pct <= 50) {
        barClass = 'bar-warning';
        textClass = 'text-warning';
      }

      let cooldownHtml = '';
      if (isDepleted && bucket.reset_time) {
        const remainingSec = getRemainingSeconds(bucket.reset_time);
        const cooldownStr = formatRemainingDuration(remainingSec);
        const isReady = remainingSec <= 0;
        cooldownHtml = `
          <div class="cooldown-alert" data-reset-time="${escapeHtml(bucket.reset_time)}" style="${isReady ? 'color: var(--color-success);' : ''}">
            <span class="cooldown-dot ${isReady ? 'ready' : ''}"></span>
            <span>Cooldown: <span class="cooldown-timer tabular-nums">${escapeHtml(cooldownStr)}</span></span>
          </div>
        `;
      }

      return `
        <div class="quota-row">
          <div class="quota-labels">
            <span class="quota-name">${escapeHtml(label)}</span>
            <span class="quota-pct ${textClass} tabular-nums">${pct}%</span>
          </div>
          <div class="quota-bar-track">
            <div class="quota-bar-fill ${barClass}" style="width: ${pct}%;"></div>
          </div>
          ${cooldownHtml}
        </div>
      `;
    }

    // Per-category interactive routing state (Bucket Borders & In-Header Controls)
    const activeCats = acc.active_categories || [];
    const disabledCats = acc.disabled_categories || [];

    function getCategoryRouting(catKey, catLabel) {
      const isPinned = activeCats.includes(catKey);
      const isDisabled = disabledCats.includes(catKey);
      const catMode = (acc.category_modes && acc.category_modes[catKey]) || rawCategoryModes[catKey] || 'pool';

      let pillClass = '';
      let groupClass = '';
      let actionIcon = '';
      let actionTitle = '';
      let nextMode = '';
      let nextEnabled = false;
      let isInPool = false;

      if (isPinned) {
        if (catMode === 'solo') {
          pillClass = 'state-active-solo';
          groupClass = 'bucket-active-solo';
          actionIcon = '🔒';
          actionTitle = 'Solo Mode active (rotation blocked). Click to switch to Pool';
          nextMode = 'pool';
          isInPool = false;
        } else {
          pillClass = 'state-active-pool';
          groupClass = 'bucket-active-pool';
          actionIcon = '🔄';
          actionTitle = 'Pool Mode active (auto-rotates). Click to switch to Solo';
          nextMode = 'solo';
          isInPool = true;
        }
      } else {
        if (isDisabled) {
          pillClass = 'state-excluded';
          groupClass = 'bucket-excluded';
          actionIcon = '🚫';
          actionTitle = 'Excluded from category failover. Click to re-enable';
          nextEnabled = true;
          isInPool = false;
        } else if (catMode === 'solo') {
          pillClass = 'state-standby-solo';
          groupClass = 'bucket-standby-solo';
          actionIcon = '🔒';
          actionTitle = 'Solo Mode active on serving account (rotation blocked). Click to switch to Pool';
          nextMode = 'pool';
          isInPool = false;
        } else {
          pillClass = 'state-standby';
          groupClass = 'bucket-standby-pool';
          actionIcon = '✓';
          actionTitle = 'Standby failover target. Click to exclude from pool';
          nextEnabled = false;
          isInPool = true;
        }
      }

      const pillHtml = `
        <div class="category-pin-pill ${pillClass}">
          <button type="button" class="pill-label-zone"
                  data-id="${escapeHtml(acc.id)}"
                  data-category="${catKey}"
                  title="${isPinned ? `${catLabel} is currently serving traffic on this account` : `Click to activate ${catLabel} on this account`}"
                  aria-label="${isPinned ? `${catLabel} active on ${escapeHtml(acc.id.slice(0, 8))}` : `Activate ${catLabel} on ${escapeHtml(acc.id.slice(0, 8))}`}">
            <span class="pin-icon">${isPinned ? '●' : (isDisabled ? '🚫' : '○')}</span>
            <span class="pin-label">${isPinned ? 'Active' : 'Activate'}</span>
          </button>
          <button type="button" class="pill-action-zone"
                  data-id="${escapeHtml(acc.id)}"
                  data-category="${catKey}"
                  data-is-pinned="${isPinned ? 'true' : 'false'}"
                  data-next-mode="${nextMode}"
                  data-next-enabled="${nextEnabled ? 'true' : 'false'}"
                  title="${actionTitle}"
                  aria-label="${actionTitle}">
            <span class="action-icon">${actionIcon}</span>
          </button>
        </div>
      `;

      return { groupClass, pillHtml, isInPool };
    }

    const claudeRouting = getCategoryRouting('claude_gpt', 'Claude/GPT');
    const geminiRouting = getCategoryRouting('gemini', 'Gemini');
    const argonRouting = getCategoryRouting('gemini_argon', 'Argon');

    // Model rules categorized by bucket
    const rules = acc.model_rules || [];

    function categorizeRule(r) {
      const mid = (r.model_id || '').toLowerCase();
      const cat = (r.category || '').toLowerCase();
      if (mid.includes('disabled')) {
        return null;
      }
      if (cat === 'claude_gpt' || /claude|gpt|sonnet|opus|haiku|3p|openai|anthropic/i.test(mid)) {
        return 'claude';
      }
      if (cat === 'gemini_argon' || /argon/i.test(mid)) {
        return 'argon';
      }
      if (/^chat_|^tab_|preview|flash-image|-tiered|gemini-3-flash$|gemini-3\.5-flash|gemini-3\.1-flash|gemini-2\.5/i.test(mid)) {
        return 'preview';
      }
      return 'gemini';
    }

    function parseModelTierInfo(r) {
      const rawMid = (r.model_id || '').trim();
      let dName = (r.display_name || rawMid).trim();
      let baseName = dName;
      let tier = '';

      // 1. If dName is raw slug like "gemini-3.8-flash-tiered", humanize baseName & extract tier
      if (/^[a-z0-9_.-]+$/.test(dName) && dName.includes('-')) {
        const parts = dName.split('-');
        const lastPart = parts[parts.length - 1].toLowerCase();
        if (['high', 'medium', 'med', 'low', 'thinking', 'preview', 'tiered', 'image'].includes(lastPart)) {
          tier = lastPart.charAt(0).toUpperCase() + lastPart.slice(1);
          parts.pop();
        }
        baseName = parts.map(p => {
          const pl = p.toLowerCase();
          if (pl === 'gemini') return 'Gemini';
          if (pl === 'claude') return 'Claude';
          if (pl === 'gpt') return 'GPT';
          if (pl === 'pro') return 'Pro';
          if (pl === 'flash') return 'Flash';
          if (pl === 'lite') return 'Lite';
          if (pl === 'argon') return 'Argon';
          return p.charAt(0).toUpperCase() + p.slice(1);
        }).join(' ');
      }

      // 2. Check for explicit standard tier in parentheses: (High|Medium|Med|Low|Thinking|Think|Preview|Tiered|Image)
      const tierMatch = baseName.match(/\s*\((High|Medium|Med|Low|Thinking|Think|Preview|Tiered|Image)\)/i);
      if (tierMatch) {
        tier = tierMatch[1];
        tier = tier.charAt(0).toUpperCase() + tier.slice(1).toLowerCase();
        baseName = baseName.replace(/\s*\((High|Medium|Med|Low|Thinking|Think|Preview|Tiered|Image)\)/i, '').trim();
      } else {
        // 3. Check for disambiguated model ID in parentheses like (gemini-2.5-flash-lite)
        const disambigMatch = baseName.match(/\s*\(([a-z0-9_.-]+)\)$/i);
        if (disambigMatch) {
          const innerSlug = disambigMatch[1].toLowerCase();
          if (innerSlug.includes('2.5')) tier = '2.5';
          else if (innerSlug.includes('3.5')) tier = '3.5';
          else if (innerSlug.includes('3.8')) tier = '3.8';
          else if (innerSlug.includes('thinking')) tier = 'Think';
          else if (innerSlug.includes('preview')) tier = 'Preview';
          else tier = 'Std';
          baseName = baseName.replace(/\s*\([a-z0-9_.-]+\)$/i, '').trim();
        }
      }

      // 4. Fallback: inspect model_id suffix if tier is still empty
      if (!tier) {
        const mid = rawMid.toLowerCase();
        if (mid.endsWith('-high')) tier = 'High';
        else if (mid.endsWith('-medium') || mid.endsWith('-med')) tier = 'Med';
        else if (mid.endsWith('-low')) tier = 'Low';
        else if (mid.endsWith('-thinking')) tier = 'Thinking';
        else if (mid.endsWith('-preview')) tier = 'Preview';
        else if (mid.endsWith('-tiered')) tier = 'Tiered';
      }

      return { baseName, tier };
    }

    function formatTierLabel(tier) {
      const t = tier.toLowerCase();
      if (t === 'medium') return 'Med';
      if (t === 'thinking') return 'Think';
      return tier;
    }

    const groupedRules = { claude: [], gemini: [], argon: [], preview: [] };
    rules.forEach(r => {
      const g = categorizeRule(r);
      if (g && groupedRules[g]) {
        groupedRules[g].push(r);
      }
    });

    function buildModelGroupRows(ruleList, groupKey) {
      if (!ruleList || ruleList.length === 0) return { count: 0, html: '' };
      const tierWeights = { 'low': 1, 'medium': 2, 'med': 2, 'high': 3, 'thinking': 4, 'think': 4, '2.5': 5, '3.5': 6, '3.8': 7, 'preview': 8, 'tiered': 9, 'image': 10 };
      const baseModelMap = new Map();
      ruleList.forEach(r => {
        const { baseName, tier } = parseModelTierInfo(r);
        if (!baseModelMap.has(baseName)) {
          baseModelMap.set(baseName, { baseName, rules: [], tiers: [] });
        }
        const item = baseModelMap.get(baseName);
        item.rules.push(r);
        if (tier) {
          if (!item.tiers.some(t => t.tier.toLowerCase() === tier.toLowerCase())) {
            item.tiers.push({ tier, rule: r });
          }
        }
      });

      baseModelMap.forEach(item => {
        item.tiers.sort((a, b) => (tierWeights[a.tier.toLowerCase()] || 99) - (tierWeights[b.tier.toLowerCase()] || 99));
      });

      const rowsHtml = Array.from(baseModelMap.values()).map(item => {
        const anyEnabled = item.rules.some(r => r.enabled);
        const allEnabled = item.rules.every(r => r.enabled);
        const modelIds = item.rules.map(r => r.model_id).join(',');

        let badgesHtml = '';
        if (item.tiers.length > 0) {
          badgesHtml = `
            <div class="model-tier-badges">
              ${item.tiers.map(({ tier, rule }) => `
                <button type="button" class="model-tier-badge ${rule.enabled ? 'is-active' : ''}"
                        data-model-id="${escapeHtml(rule.model_id)}"
                        data-tier="${escapeHtml(tier)}"
                        title="${escapeHtml(rule.display_name || rule.model_id)} (${rule.enabled ? 'Enabled' : 'Disabled'})">
                  ${escapeHtml(formatTierLabel(tier))}
                </button>
              `).join('')}
            </div>
          `;
        }

        let fallbackHtml = '';
        const isFallbackActive = Boolean(currentAppConfig.fallback_secondary_enabled);
        const isPrimary = isFallbackActive && item.rules.some(r => r.model_id === currentAppConfig.model_primary);
        const isSecondary = isFallbackActive && item.rules.some(r => r.model_id === currentAppConfig.model_secondary);

        if (isPrimary) {
          const secModel = rawModelsData.find(m => m.id === currentAppConfig.model_secondary);
          const secName = secModel?.display_name || currentAppConfig.model_secondary;
          fallbackHtml = `
            <button type="button" class="model-fallback-btn is-active"
                    data-action="configure-fallback"
                    data-model-ids="${escapeHtml(modelIds)}"
                    data-base-name="${escapeHtml(item.baseName)}"
                    title="Active 429 Fallback: Retries on ${escapeHtml(secName)} within same account first">
              ↪ ${escapeHtml(secName)}
            </button>
          `;
        } else if (isSecondary) {
          fallbackHtml = `
            <span class="model-fallback-badge" title="Target fallback model when primary hits 429">
              fallback target
            </span>
          `;
        } else {
          fallbackHtml = `
            <button type="button" class="model-fallback-btn"
                    data-action="set-fallback"
                    data-model-ids="${escapeHtml(modelIds)}"
                    data-base-name="${escapeHtml(item.baseName)}"
                    title="Set 429 fallback for ${escapeHtml(item.baseName)}">
              ↪
            </button>
          `;
        }

        return `
          <div class="model-rule-row ${anyEnabled ? '' : 'model-rule-disabled'}" data-model-ids="${escapeHtml(modelIds)}" data-group="${groupKey}">
            <label class="model-rule-name" title="${escapeHtml(modelIds)}">
              <input type="checkbox" class="model-rule-enabled" ${allEnabled ? 'checked' : ''} ${anyEnabled && !allEnabled ? 'data-indeterminate="true"' : ''}>
              <span class="model-label-text">${escapeHtml(item.baseName)}</span>
            </label>
            ${badgesHtml}
            ${fallbackHtml}
          </div>
        `;
      }).join('');

      return { count: baseModelMap.size, html: rowsHtml };
    }

    function renderBucketModels(groupKey, rulesForGroup) {
      const std = buildModelGroupRows(rulesForGroup, groupKey);

      if (std.count === 0) {
        return '';
      }

      return `
        <div class="bucket-models-section" data-group="${groupKey}">
          <div class="bucket-models-header">
            <span class="bucket-models-title">Models (${std.count})</span>
            <div class="bucket-models-actions">
              <button type="button" class="btn-group-toggle" data-action="all" data-group="${groupKey}">All</button>
              <button type="button" class="btn-group-toggle" data-action="none" data-group="${groupKey}">None</button>
            </div>
          </div>
          <div class="bucket-models-subtext">Unchecked models skip this account.</div>
          <div class="bucket-models-rows">
            ${std.html}
          </div>
        </div>
      `;
    }

    const claudeModelsHtml = renderBucketModels('claude', groupedRules.claude);
    const geminiModelsHtml = renderBucketModels('gemini', groupedRules.gemini);
    const argonModelsHtml = renderBucketModels('argon', groupedRules.argon);
    const hasArgonAccess = Boolean(argonWeekly) ||
      (groupedRules.argon && groupedRules.argon.length > 0) ||
      rules.some(r => /5-5/i.test(r.model_id) || (/argon/i.test(r.model_id) && !r.model_id.includes('disabled')));

    // Build Model Groups with Bucket-Level Borders
    let extraHtml = '';
    if (extraBuckets.length > 0) {
      extraHtml = `
        <div class="quota-group">
          <div class="quota-group-header">
            <span class="quota-group-name">Additional Quota Limits</span>
          </div>
          ${extraBuckets.map(b => renderQuotaRow(b.display_name || b.bucket_id, b)).join('')}
        </div>
      `;
    }

    const quotaBodyHtml = `
      <!-- Group 1: Claude & GPT Models (3P) -->
      <div class="quota-group ${claudeRouting.groupClass}" data-category-group="claude_gpt" data-in-pool="${claudeRouting.isInPool ? 'true' : 'false'}">
        <div class="quota-group-header">
          <span class="quota-group-name">Claude & GPT Models</span>
          ${claudeRouting.pillHtml}
        </div>
        ${renderQuotaRow('5-Hour Sliding Limit', claude5h)}
        ${renderQuotaRow('Weekly Sliding Limit', claudeWeekly)}
        ${claudeModelsHtml}
      </div>

      <!-- Group 2: Gemini Models -->
      <div class="quota-group ${geminiRouting.groupClass}" data-category-group="gemini" data-in-pool="${geminiRouting.isInPool ? 'true' : 'false'}">
        <div class="quota-group-header">
          <span class="quota-group-name">Gemini Models</span>
          ${geminiRouting.pillHtml}
        </div>
        ${renderQuotaRow('5-Hour Sliding Limit', gemini5h)}
        ${renderQuotaRow('Weekly Sliding Limit', geminiWeekly)}
        ${geminiModelsHtml}
      </div>

      <!-- Group 3: Gemini 4 Argon -->
      ${hasArgonAccess ? `
      <div class="quota-group ${argonRouting.groupClass}" data-category-group="gemini_argon" data-in-pool="${argonRouting.isInPool ? 'true' : 'false'}">
        <div class="quota-group-header">
          <span class="quota-group-name">Gemini 4 Argon</span>
          ${argonRouting.pillHtml}
        </div>
        ${renderQuotaRow('Weekly Sliding Limit', argonWeekly)}
        ${argonModelsHtml}
      </div>
      ` : `
      <div class="quota-group bucket-unentitled" data-category-group="gemini_argon" data-in-pool="false">
        <div class="quota-group-header">
          <span class="quota-group-name">Gemini 4 Argon</span>
          <span class="badge-unentitled" title="This account does not have a paid Pro plan with Argon access">No Plan Access</span>
        </div>
        <div class="quota-row-unentitled">Not available on this account tier</div>
      </div>
      `}

      ${extraHtml}
    `;

    // Status badge formatting
    let statusBadgeClass = 'badge-neutral';
    let statusText = 'STANDBY';
    if (isExhausted) {
      statusBadgeClass = 'badge-danger';
      statusText = 'EXHAUSTED';
    } else if (acc.status === 'error') {
      statusBadgeClass = 'badge-danger';
      statusText = 'ERROR';
    } else if (acc.status === 'disabled') {
      statusBadgeClass = 'badge-neutral';
      statusText = 'DISABLED';
    } else if (activeCats.length > 0) {
      statusBadgeClass = 'badge-success';
      statusText = `ROUTING (${activeCats.length}/3)`;
    }

    const cardTitle = isPrivacyMode ? '[Protected in Privacy Mode]' : escapeHtml(acc.email);
    const copyTitle = isPrivacyMode ? 'Copy redacted email' : 'Copy email address';

    card.innerHTML = `
      <div class="account-header">
        <div class="account-info">
          <div class="account-email" title="${cardTitle}">
            ${escapeHtml(acc.email)}
          </div>
          <div class="account-id-row">
            <span class="account-id-chip">ID: ${escapeHtml(acc.id.slice(0, 8))}</span>
            <button class="copy-btn btn-copy-email" data-email="${escapeHtml(acc.email)}" title="${copyTitle}" aria-label="Copy email">
              <svg width="12" height="12" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
              </svg>
            </button>
          </div>
        </div>
        <div class="badge ${statusBadgeClass}">
          ${escapeHtml(statusText)}
        </div>
      </div>

      <div class="quota-matrix">
        ${quotaBodyHtml}
      </div>

      <div class="account-actions">
        ${activeCats.length === 3 ? `
          <div class="active-pill" aria-current="true">
            All Categories Active
          </div>
        ` : `
          <button class="btn btn-secondary btn-switch-account" data-id="${escapeHtml(acc.id)}" style="flex: 1;" title="Activate all model categories on this account">
            Activate All Categories
          </button>
        `}
        <button class="btn btn-danger-subtle btn-delete-account" data-id="${escapeHtml(acc.id)}" title="Remove Account" aria-label="Remove Account">
          <svg width="15" height="15" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
          </svg>
        </button>
      </div>
    `;

    // Bind copy email
    const copyBtn = card.querySelector('.btn-copy-email');
    if (copyBtn) {
      copyBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        const toCopy = isPrivacyMode ? '[redacted@email.com]' : acc.email;
        copyToClipboard(toCopy, isPrivacyMode ? 'Redacted email' : 'Account email');
      });
    }

    // Bind switch button
    const switchBtn = card.querySelector('.btn-switch-account');
    if (switchBtn) {
      switchBtn.addEventListener('click', () => handleSwitchAccount(acc.id));
    }

    card.querySelectorAll('.pill-label-zone').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        handleSwitchAccount(btn.dataset.id, btn.dataset.category);
      });
    });

    card.querySelectorAll('.pill-action-zone').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        const id = btn.dataset.id;
        const cat = btn.dataset.category;
        const nextMode = btn.dataset.nextMode;
        if (nextMode) {
          const activeAccount = rawAccountsData.find(a => (a.active_categories || []).includes(cat));
          const targetId = activeAccount ? activeAccount.id : id;
          handleToggleCategoryMode(targetId, cat, nextMode);
        } else {
          handleToggleCategoryExclusion(id, cat, btn.dataset.nextEnabled === 'true');
        }
      });
    });

    // Bind model rules toggles and fallbacks across the card
    card.querySelectorAll('.model-rule-row').forEach(row => {
      const cb = row.querySelector('.model-rule-enabled');
      if (cb && cb.dataset.indeterminate === 'true') {
        cb.indeterminate = true;
      }

      const modelIds = (row.dataset.modelIds || '').split(',').filter(Boolean);

      cb.addEventListener('change', async e => {
        cb.indeterminate = false;
        row.classList.toggle('model-rule-disabled', !e.target.checked);
        row.querySelectorAll('.model-tier-badge').forEach(b => {
          b.classList.toggle('is-active', e.target.checked);
        });
        const promises = modelIds.map(mId => saveModelRule(acc.id, { model_id: mId, enabled: e.target.checked }, false));
        await Promise.all(promises);
        await fetchAccounts();
      });

      row.querySelectorAll('.model-tier-badge').forEach(badge => {
        badge.addEventListener('click', async e => {
          e.preventDefault();
          e.stopPropagation();
          const mId = badge.dataset.modelId;
          const isCurrentlyActive = badge.classList.contains('is-active');
          badge.classList.toggle('is-active', !isCurrentlyActive);
          await saveModelRule(acc.id, { model_id: mId, enabled: !isCurrentlyActive }, false);
          await fetchAccounts();
        });
      });

      const fallbackBtn = row.querySelector('.model-fallback-btn');
      if (fallbackBtn) {
        fallbackBtn.addEventListener('click', e => {
          e.preventDefault();
          e.stopPropagation();
          openFallbackModal(fallbackBtn.dataset.modelIds, fallbackBtn.dataset.baseName);
        });
      }
    });

    card.querySelectorAll('.btn-group-toggle').forEach(btn => {
      btn.addEventListener('click', async e => {
        e.preventDefault();
        e.stopPropagation();
        const action = btn.dataset.action;
        const groupKey = btn.dataset.group;
        const enabled = action === 'all';
        const targetRules = groupedRules[groupKey] || [];
        const promises = [];
        for (const r of targetRules) {
          if (r.enabled !== enabled) {
            promises.push(saveModelRule(acc.id, { model_id: r.model_id, enabled }, false));
          }
        }
        if (promises.length > 0) {
          await Promise.all(promises);
          await fetchAccounts();
        }
      });
    });

    // Bind safe two-step delete button (inline confirmation without window.confirm)
    const deleteBtn = card.querySelector('.btn-delete-account');
    if (deleteBtn) {
      let confirmTimeout = null;
      deleteBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        if (deleteBtn.classList.contains('btn-danger-confirm')) {
          clearTimeout(confirmTimeout);
          handleDeleteAccount(acc.id, acc.email);
        } else {
          deleteBtn.classList.add('btn-danger-confirm');
          deleteBtn.title = 'Click again to confirm removal';
          deleteBtn.innerHTML = `
            <svg width="15" height="15" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M5 13l4 4L19 7" />
            </svg>
          `;
          const displayEmail = isPrivacyMode ? '[redacted@email.com]' : acc.email;
          showToast(`Click the red checkmark to confirm removal of ${displayEmail}`, 'info', 3500);

          confirmTimeout = setTimeout(() => {
            deleteBtn.classList.remove('btn-danger-confirm');
            deleteBtn.title = 'Remove Account';
            deleteBtn.innerHTML = `
              <svg width="15" height="15" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
              </svg>
            `;
          }, 3500);
        }
      });
    }

    return card;
  }

  // Live real-time cooldown ticker (ticks every 1s without HTTP requests)
  function updateCooldownTimers() {
    const alerts = document.querySelectorAll('.cooldown-alert[data-reset-time]');
    alerts.forEach(alertEl => {
      const resetTimeStr = alertEl.getAttribute('data-reset-time');
      const timerSpan = alertEl.querySelector('.cooldown-timer');
      const dot = alertEl.querySelector('.cooldown-dot');
      if (timerSpan && resetTimeStr) {
        const remainingSec = getRemainingSeconds(resetTimeStr);
        if (remainingSec <= 0) {
          timerSpan.textContent = 'Reset ready';
          alertEl.style.color = 'var(--color-success)';
          if (dot) dot.classList.add('ready');
        } else {
          timerSpan.textContent = formatRemainingDuration(remainingSec);
          if (dot) dot.classList.remove('ready');
        }
      }
    });
  }

  // =========================================================================
  // Model Fallback Configuration & Dynamic Discovery
  // =========================================================================

  async function fetchConfig() {
    try {
      const res = await fetch('/api/config');
      if (!res.ok) return;
      currentAppConfig = await res.json();
    } catch (err) {
      console.warn('Failed to fetch config:', err);
    }
  }

  async function fetchModels() {
    if (refreshModelsIcon) {
      refreshModelsIcon.style.transition = 'transform 0.5s ease-in-out';
      refreshModelsIcon.style.transform = 'rotate(360deg)';
    }

    try {
      const res = await fetch('/api/models');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      rawModelsData = Array.isArray(data.models) ? data.models : [];

      if (modelsSourceBadge) {
        if (data.source === 'language_server') {
          modelsSourceBadge.textContent = 'Antigravity IDE (Live)';
          modelsSourceBadge.className = 'badge badge-success mono';
          modelsSourceBadge.title = 'Discovered dynamically from running language_server';
        } else if (data.source === 'quota_buckets') {
          modelsSourceBadge.textContent = 'Quota Buckets';
          modelsSourceBadge.className = 'badge badge-info mono';
        } else {
          modelsSourceBadge.textContent = 'Standard Catalog';
          modelsSourceBadge.className = 'badge badge-neutral mono';
        }
      }

      renderAccounts();
    } catch (err) {
      console.warn('Failed to fetch models:', err);
      if (modelsSourceBadge) {
        modelsSourceBadge.textContent = 'Catalog (Offline)';
        modelsSourceBadge.className = 'badge badge-warning mono';
      }
    } finally {
      setTimeout(() => {
        if (refreshModelsIcon) {
          refreshModelsIcon.style.transition = 'none';
          refreshModelsIcon.style.transform = 'rotate(0deg)';
        }
      }, 500);
    }
  }

  function getModelCategory(modelId) {
    const found = rawModelsData.find(m => m.id === modelId);
    if (found && found.category) return found.category;
    const lower = (modelId || '').toLowerCase();
    if (lower.includes('claude') || lower.includes('gpt') || lower.includes('sonnet') || lower.includes('opus') || lower.includes('haiku') || lower.includes('3p')) {
      return 'claude_gpt';
    }
    if (lower.includes('argon')) return 'gemini_argon';
    return 'gemini';
  }

  function openFallbackModal(modelIdsStr, baseName) {
    const modal = document.getElementById('fallback-modal');
    if (!modal) return;

    const primaryDisplay = document.getElementById('modal-primary-display');
    const tierGroup = document.getElementById('modal-primary-tier-group');
    const selectPrimary = document.getElementById('modal-select-primary');
    const selectSecondary = document.getElementById('modal-select-secondary');
    const btnDisable = document.getElementById('btn-disable-fallback');

    if (primaryDisplay) primaryDisplay.textContent = baseName;

    const ids = (modelIdsStr || '').split(',').filter(Boolean);
    if (ids.length > 1 && tierGroup && selectPrimary) {
      tierGroup.style.display = 'block';
      selectPrimary.innerHTML = ids.map(id => {
        const m = rawModelsData.find(rm => rm.id === id);
        const label = m?.display_name || id;
        const isSel = id === currentAppConfig.model_primary;
        return `<option value="${escapeHtml(id)}" ${isSel ? 'selected' : ''}>${escapeHtml(label)}</option>`;
      }).join('');
    } else if (selectPrimary) {
      if (tierGroup) tierGroup.style.display = 'none';
      const singleId = ids[0] || '';
      const m = rawModelsData.find(rm => rm.id === singleId);
      selectPrimary.innerHTML = `<option value="${escapeHtml(singleId)}" selected>${escapeHtml(m?.display_name || baseName)}</option>`;
    }

    function populateSecondarySelect() {
      if (!selectSecondary || !selectPrimary) return;
      const currentPri = selectPrimary.value;
      const currentSec = currentAppConfig.model_secondary;

      const allowed = rawModelsData.filter(m => m.id !== currentPri && !categorizeRule(m)?.includes('preview'));

      const groups = { claude_gpt: [], gemini: [], other: [] };
      allowed.forEach(m => {
        if (m.category === 'claude_gpt') groups.claude_gpt.push(m);
        else if (m.category === 'gemini') groups.gemini.push(m);
        else groups.other.push(m);
      });

      let html = '';
      if (groups.claude_gpt.length > 0) {
        html += '<optgroup label="Claude & GPT (3P)">';
        groups.claude_gpt.forEach(m => {
          const sel = m.id === currentSec ? 'selected' : '';
          const star = m.recommended ? ' ★' : '';
          html += `<option value="${escapeHtml(m.id)}" ${sel}>${escapeHtml(m.display_name || m.id)}${star}</option>`;
        });
        html += '</optgroup>';
      }
      if (groups.gemini.length > 0) {
        html += '<optgroup label="Google Gemini">';
        groups.gemini.forEach(m => {
          const sel = m.id === currentSec ? 'selected' : '';
          const star = m.recommended ? ' ★' : '';
          html += `<option value="${escapeHtml(m.id)}" ${sel}>${escapeHtml(m.display_name || m.id)}${star}</option>`;
        });
        html += '</optgroup>';
      }
      if (groups.other.length > 0) {
        html += '<optgroup label="Other Models">';
        groups.other.forEach(m => {
          const sel = m.id === currentSec ? 'selected' : '';
          html += `<option value="${escapeHtml(m.id)}" ${sel}>${escapeHtml(m.display_name || m.id)}</option>`;
        });
        html += '</optgroup>';
      }

      selectSecondary.innerHTML = html;
    }

    populateSecondarySelect();
    if (selectPrimary) {
      selectPrimary.onchange = populateSecondarySelect;
    }

    const isCurrentlyActivePrimary = currentAppConfig.fallback_secondary_enabled && ids.includes(currentAppConfig.model_primary);
    if (btnDisable) {
      btnDisable.style.display = isCurrentlyActivePrimary ? 'inline-block' : 'none';
    }

    modal.showModal();
  }

  async function saveFallbackPolicy(primaryId, secondaryId, enabled) {
    const payload = {
      model_primary: primaryId,
      model_secondary: secondaryId,
      fallback_secondary_enabled: enabled
    };

    const res = await fetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });

    if (!res.ok) {
      const errData = await res.json().catch(() => ({}));
      throw new Error(errData?.error?.message || `HTTP ${res.status}`);
    }

    const updated = await res.json();
    currentAppConfig = updated;
    const priDisplay = rawModelsData.find(m => m.id === updated.model_primary)?.display_name || updated.model_primary;
    const secDisplay = rawModelsData.find(m => m.id === updated.model_secondary)?.display_name || updated.model_secondary;
    if (enabled) {
      showToast(`Model fallback active: ${priDisplay} ↪ ${secDisplay}`, 'success', 3000);
    } else {
      showToast('Model fallback disabled', 'info', 2500);
    }
    renderAccounts();
  }

  function setupFallbackModalEvents() {
    const modal = document.getElementById('fallback-modal');
    if (!modal) return;

    const btnClose = document.getElementById('btn-close-fallback-modal');
    const btnCancel = document.getElementById('btn-cancel-fallback');
    const btnSave = document.getElementById('btn-save-fallback');
    const btnDisable = document.getElementById('btn-disable-fallback');
    const selectPrimary = document.getElementById('modal-select-primary');
    const selectSecondary = document.getElementById('modal-select-secondary');

    const closeModal = () => modal.close();

    if (btnClose) btnClose.onclick = closeModal;
    if (btnCancel) btnCancel.onclick = closeModal;

    modal.addEventListener('click', e => {
      if (e.target === modal) modal.close();
    });

    if (btnSave) {
      btnSave.onclick = async () => {
        const pri = selectPrimary?.value;
        const sec = selectSecondary?.value;
        if (!pri || !sec) {
          showToast('Please select both primary and fallback models', 'warning', 3000);
          return;
        }
        if (pri === sec) {
          showToast('Primary and fallback models must be distinct', 'warning', 3000);
          return;
        }
        btnSave.disabled = true;
        btnSave.textContent = 'Saving...';
        try {
          await saveFallbackPolicy(pri, sec, true);
          modal.close();
        } finally {
          btnSave.disabled = false;
          btnSave.textContent = 'Save Policy';
        }
      };
    }

    if (btnDisable) {
      btnDisable.onclick = async () => {
        btnDisable.disabled = true;
        btnDisable.textContent = 'Removing...';
        try {
          await saveFallbackPolicy(currentAppConfig.model_primary, currentAppConfig.model_secondary, false);
          modal.close();
        } finally {
          btnDisable.disabled = false;
          btnDisable.textContent = 'Remove Fallback';
        }
      };
    }
  }

  // =========================================================================
  // Token Metrics & 14-Day Timeline Visualizer (Bug Fix & Redesign)
  // =========================================================================

  async function fetchMetrics() {
    try {
      const tzInfo = getClientTimezoneInfo();
      const res = await fetch(
        `/api/metrics?period=${encodeURIComponent(currentPeriod)}` +
        `&tz=${encodeURIComponent(tzInfo.tz)}` +
        `&tz_offset=${encodeURIComponent(tzInfo.offsetMinutes)}`
      );
      if (!res.ok) return;
      const data = await res.json();

      // Quick Stats
      if (data.summary) {
        const todayTokens = data.summary.today?.total_tokens ?? 0;
        const todayReqs = data.summary.today?.total_requests ?? 0;
        const allTimeTokens = data.summary.all_time?.total_tokens ?? 0;
        const allTimeReqs = data.summary.all_time?.total_requests ?? 0;

        const tokensTodayEl = document.getElementById('tokens-today');
        if (tokensTodayEl) {
          tokensTodayEl.textContent = formatCompactNumber(todayTokens);
          tokensTodayEl.title = `${formatNumber(todayTokens)} tokens`;
        }

        const requestsTodayEl = document.getElementById('requests-today');
        if (requestsTodayEl) {
          requestsTodayEl.textContent = `${formatNumber(todayReqs)} requests today`;
        }

        const tokensAllTimeEl = document.getElementById('tokens-alltime');
        if (tokensAllTimeEl) {
          tokensAllTimeEl.textContent = formatCompactNumber(allTimeTokens);
          tokensAllTimeEl.title = `${formatNumber(allTimeTokens)} tokens`;
        }

        const requestsAllTimeEl = document.getElementById('requests-alltime');
        if (requestsAllTimeEl) {
          requestsAllTimeEl.textContent = `${formatNumber(allTimeReqs)} requests all time`;
        }

        // Active period metrics tiles
        let periodSummary = data.summary.all_time;
        if (currentPeriod === 'day') periodSummary = data.summary.today;
        else if (currentPeriod === 'week') periodSummary = data.summary.this_week;
        else if (currentPeriod === 'month') periodSummary = data.summary.this_month;

        if (periodSummary) {
          const promptEl = document.getElementById('metric-prompt');
          if (promptEl) {
            promptEl.textContent = formatCompactNumber(periodSummary.total_prompt_tokens);
            promptEl.title = `${formatNumber(periodSummary.total_prompt_tokens)} prompt tokens`;
          }

          const candEl = document.getElementById('metric-candidates');
          if (candEl) {
            candEl.textContent = formatCompactNumber(periodSummary.total_candidates_tokens);
            candEl.title = `${formatNumber(periodSummary.total_candidates_tokens)} candidates tokens`;
          }

          const cachedEl = document.getElementById('metric-cached');
          if (cachedEl) {
            cachedEl.textContent = formatCompactNumber(periodSummary.total_cached_content_tokens);
            cachedEl.title = `${formatNumber(periodSummary.total_cached_content_tokens)} cached context tokens`;
          }

          const reqsEl = document.getElementById('metric-requests');
          if (reqsEl) {
            reqsEl.textContent = formatNumber(periodSummary.total_requests);
          }
        }
      }

      // Render 14-Day Timeline Chart
      rawTimelineData = Array.isArray(data.timeline) ? data.timeline : [];
      renderTimelineChart(rawTimelineData);
    } catch (e) {
      console.warn('Failed to fetch metrics:', e);
    }
  }

  // Comprehensive Chart & Tooltip Implementation
  function renderTimelineChart(timeline) {
    if (!barsTrack || !chartXAxis) return;

    barsTrack.innerHTML = '';
    chartXAxis.innerHTML = '';
    hideChartTooltip();

    if (!timeline || timeline.length === 0) {
      barsTrack.innerHTML = `
        <div style="width: 100%; height: 100%; display: flex; align-items: center; justify-content: center;" class="text-muted text-xs">
          No requests recorded in this 14-day window.
        </div>
      `;
      if (gridMaxLabel) gridMaxLabel.textContent = '--';
      if (gridMidLabel) gridMidLabel.textContent = '--';
      return;
    }

    const maxTokens = Math.max(10, ...timeline.map(d => d.total_tokens || 0));

    // Update Y-Axis Scale Labels
    if (gridMaxLabel) gridMaxLabel.textContent = formatCompactNumber(maxTokens);
    if (gridMidLabel) gridMidLabel.textContent = formatCompactNumber(Math.round(maxTokens / 2));

    const todayStr = getLocalDateStr();

    // Render columns
    timeline.forEach((item, index) => {
      const total = item.total_tokens || 0;
      const prompt = item.prompt_tokens || 0;
      const candidates = item.candidates_tokens || 0;
      const reqs = item.request_count || 0;

      const column = document.createElement('div');
      column.className = 'chart-column';
      column.setAttribute('tabindex', '0');
      column.setAttribute('role', 'button');
      column.setAttribute('data-index', index);
      column.setAttribute('aria-label', `${item.date}: ${formatNumber(total)} tokens across ${reqs} requests`);

      // Height calculation
      let barHeightPct = 0;
      if (total > 0) {
        barHeightPct = Math.max(4, Math.round((total / maxTokens) * 100));
      }

      // Proportional prompt vs candidates inside stacked bar with guaranteed minimum visible slice
      let candidatesHeightPct = 0;
      let promptHeightPct = 100;

      if (total > 0 && candidates > 0) {
        const rawRatio = candidates / total;
        // Ensure candidates has at least 2% height if > 0 so it's discernible
        candidatesHeightPct = Math.max(2, Math.round(rawRatio * 100));
        promptHeightPct = 100 - candidatesHeightPct;
      }

      column.innerHTML = `
        <div class="bar-stack" style="height: ${total === 0 ? '3px' : barHeightPct + '%'}; ${total === 0 ? 'background: var(--border-strong); opacity: 0.35;' : ''}">
          ${total > 0 ? `
            <div class="bar-seg-candidates" style="height: ${candidatesHeightPct}%;"></div>
            <div class="bar-seg-prompt" style="height: ${promptHeightPct}%;"></div>
          ` : ''}
        </div>
      `;

      // Event listeners for hover, touch, click, keyboard
      const onSelect = () => selectChartColumn(index, column, item);
      column.addEventListener('pointerenter', onSelect);
      column.addEventListener('focus', onSelect);
      column.addEventListener('click', onSelect);
      column.addEventListener('keydown', (e) => handleChartKeydown(e, index));

      barsTrack.appendChild(column);

      // X-Axis Date label
      const dateLabel = document.createElement('span');
      dateLabel.className = `x-axis-label ${item.date === todayStr ? 'is-today' : ''}`;
      dateLabel.textContent = formatDateLabel(item.date);
      dateLabel.title = item.date;
      chartXAxis.appendChild(dateLabel);
    });

    // Reset inspection banner
    resetInspector();
  }

  // Handle keyboard navigation between bars
  function handleChartKeydown(e, currentIndex) {
    if (e.key === 'ArrowRight' && currentIndex < rawTimelineData.length - 1) {
      e.preventDefault();
      const nextCol = barsTrack.querySelector(`[data-index="${currentIndex + 1}"]`);
      if (nextCol) nextCol.focus();
    } else if (e.key === 'ArrowLeft' && currentIndex > 0) {
      e.preventDefault();
      const prevCol = barsTrack.querySelector(`[data-index="${currentIndex - 1}"]`);
      if (prevCol) prevCol.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      const firstCol = barsTrack.querySelector(`[data-index="0"]`);
      if (firstCol) firstCol.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      const lastCol = barsTrack.querySelector(`[data-index="${rawTimelineData.length - 1}"]`);
      if (lastCol) lastCol.focus();
    } else if (e.key === 'Escape') {
      hideChartTooltip();
      resetInspector();
    }
  }

  // Select column and display non-clipped tooltip and inspection header
  function selectChartColumn(index, columnEl, item) {
    selectedColumnIndex = index;

    // Highlight active column
    const allCols = barsTrack.querySelectorAll('.chart-column');
    allCols.forEach(col => col.classList.remove('is-selected'));
    columnEl.classList.add('is-selected');

    const total = item.total_tokens || 0;
    const prompt = item.prompt_tokens || 0;
    const candidates = item.candidates_tokens || 0;
    const reqs = item.request_count || 0;

    // 1. Update Inspection Header (Always visible, non-blocking on all devices)
    if (inspectorDate && inspectorData) {
      inspectorDate.textContent = item.date;
      inspectorData.innerHTML = `
        <div class="inspector-chip">
          <span class="text-primary font-bold tabular-nums">${formatNumber(total)}</span>
          <span class="text-muted text-xs">tokens</span>
        </div>
        <div class="inspector-chip">
          <span class="inspector-chip-dot" style="background: var(--chart-prompt);"></span>
          <span class="text-accent tabular-nums">${formatNumber(prompt)}</span>
          <span class="text-muted text-xs">prompt</span>
        </div>
        <div class="inspector-chip">
          <span class="inspector-chip-dot" style="background: var(--chart-candidates);"></span>
          <span class="text-success tabular-nums">${formatNumber(candidates)}</span>
          <span class="text-muted text-xs">candidates</span>
        </div>
        <div class="inspector-chip">
          <span class="text-primary tabular-nums">${formatNumber(reqs)}</span>
          <span class="text-muted text-xs">requests</span>
        </div>
      `;
    }

    // 2. Position Floating Tooltip relative to chartViewport with strict coordinate clamping
    if (chartTooltip && chartViewport) {
      document.getElementById('tooltip-date').textContent = item.date;
      document.getElementById('tooltip-total').textContent = formatNumber(total);
      document.getElementById('tooltip-prompt').textContent = formatNumber(prompt);
      document.getElementById('tooltip-candidates').textContent = formatNumber(candidates);
      document.getElementById('tooltip-reqs').textContent = formatNumber(reqs);

      chartTooltip.classList.add('visible');

      const viewportRect = chartViewport.getBoundingClientRect();
      const colRect = columnEl.getBoundingClientRect();

      // Tooltip is inside chartViewport, so offset relative to viewportRect
      const colCenterX = (colRect.left + colRect.width / 2) - viewportRect.left;
      const tooltipWidth = chartTooltip.offsetWidth || 155;
      const tooltipHeight = chartTooltip.offsetHeight || 85;

      // Strict horizontal clamp within viewport bounds
      const padding = 8;
      const minX = tooltipWidth / 2 + padding;
      const maxX = viewportRect.width - tooltipWidth / 2 - padding;
      const clampedX = Math.max(minX, Math.min(maxX, colCenterX));

      chartTooltip.style.left = `${clampedX}px`;

      // Smart vertical placement:
      // If there is enough room above the bar top, place above the bar
      const barTop = colRect.top - viewportRect.top;
      if (barTop > tooltipHeight + 14) {
        chartTooltip.style.top = `${Math.max(6, barTop - tooltipHeight - 6)}px`;
      } else {
        // Bar is tall: place at top edge smoothly
        chartTooltip.style.top = '8px';
      }
    }
  }

  function hideChartTooltip() {
    if (chartTooltip) {
      chartTooltip.classList.remove('visible');
    }
    const allCols = barsTrack ? barsTrack.querySelectorAll('.chart-column') : [];
    allCols.forEach(col => col.classList.remove('is-selected'));
    selectedColumnIndex = -1;
  }

  function resetInspector() {
    if (inspectorDate && inspectorData) {
      const tzInfo = getClientTimezoneInfo();
      const tzLabel = tzInfo.tz ? ` • ${tzInfo.tz}` : '';
      inspectorDate.textContent = `14-Day Daily Usage Trend${tzLabel}`;
      inspectorData.innerHTML = `
        <span class="text-xs text-muted">Hover or use arrow keys to inspect daily breakdown</span>
      `;
    }
  }

  // Pointer leave listener on chart card
  if (chartCard) {
    chartCard.addEventListener('pointerleave', () => {
      hideChartTooltip();
      resetInspector();
    });
  }

  // =========================================================================
  // Account Actions: Switch & Delete
  // =========================================================================


  async function saveModelRule(accountID, rule, shouldRefetch = true) {
    try {
      const res = await fetch(`/api/accounts/${encodeURIComponent(accountID)}/models`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(rule)
      });
      if (res.ok) {
        if (shouldRefetch) {
          showToast('Model rule saved', 'success');
        }
      } else {
        const err = await res.json().catch(() => ({}));
        showToast(err.error?.message || 'Failed to save model rule', 'error');
      }
    } catch (e) {
      showToast(`Network error saving model rule: ${e.message}`, 'error');
    }
    if (shouldRefetch) {
      await fetchAccounts();
    }
  }

  async function handleSwitchAccount(accountID, category = 'all') {
    try {
      const res = await fetch(`/api/accounts/${encodeURIComponent(accountID)}/select`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ category })
      });
      if (res.ok) {
        const what = category === 'all' ? 'Active proxy account' : `Active ${category} account`;
        showToast(`${what} switched to ${accountID.slice(0, 8)}`, 'success');
        await Promise.all([fetchStatus(), fetchAccounts()]);
      } else {
        const err = await res.json().catch(() => ({}));
        const msg = err.error?.message || 'Failed to switch account';
        showToast(msg, 'error');
      }
    } catch (e) {
      showToast(`Network error switching account: ${e.message}`, 'error');
    }
  }

  async function handleToggleCategoryMode(accountID, category, newMode) {
    try {
      const res = await fetch(`/api/accounts/${encodeURIComponent(accountID)}/select`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ category, mode: newMode })
      });
      if (res.ok) {
        showToast(`${category} mode set to ${newMode.toUpperCase()}`, 'success');
        await Promise.all([fetchStatus(), fetchAccounts()]);
      } else {
        const err = await res.json().catch(() => ({}));
        showToast(err.error?.message || 'Failed to update category mode', 'error');
      }
    } catch (e) {
      showToast(`Network error updating mode: ${e.message}`, 'error');
    }
  }

  async function handleToggleCategoryExclusion(accountID, category, enabled) {
    try {
      const res = await fetch(`/api/accounts/${encodeURIComponent(accountID)}/categories`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ category, enabled })
      });
      if (res.ok) {
        showToast(`${category} ${enabled ? 'enabled' : 'excluded'} for ${accountID.slice(0, 8)}`, 'info');
        await Promise.all([fetchStatus(), fetchAccounts()]);
      } else {
        const err = await res.json().catch(() => ({}));
        showToast(err.error?.message || 'Failed to update category exclusion', 'error');
      }
    } catch (e) {
      showToast(`Network error updating exclusion: ${e.message}`, 'error');
    }
  }

  async function handleDeleteAccount(accountID, email) {
    try {
      const res = await fetch(`/api/accounts/${encodeURIComponent(accountID)}`, {
        method: 'DELETE'
      });
      if (res.ok) {
        const displayEmail = isPrivacyMode ? '[redacted@email.com]' : email;
        showToast(`Account ${displayEmail} removed from pool`, 'info');
        await Promise.all([fetchStatus(), fetchAccounts()]);
      } else {
        const err = await res.json().catch(() => ({}));
        showToast(err.error?.message || 'Failed to remove account', 'error');
      }
    } catch (e) {
      showToast(`Network error removing account: ${e.message}`, 'error');
    }
  }

  async function handleAddAccount() {
    const btn = document.getElementById('btn-add-account');
    if (btn) btn.disabled = true;

    try {
      const res = await fetch('/oauth/start', { method: 'POST' });
      const data = await res.json().catch(() => ({}));
      if (res.ok && data.auth_url) {
        showToast('Opening Google sign-in window...', 'info', 5000);
        const popup = window.open(data.auth_url, 'antigravity_oauth', 'width=600,height=700,status=no,toolbar=no');
        if (!popup || popup.closed || typeof popup.closed === 'undefined') {
          showToast('Popup blocked by browser. Please allow popups to complete Google sign-in.', 'warning', 8000);
        }
      } else if (res.ok) {
        showToast('OAuth flow initiated. Complete Google sign-in in your browser window.', 'info', 6000);
      } else {
        const errMsg = data.error?.detail || data.error?.message || 'Failed to initiate OAuth authorization';
        showToast(errMsg, 'error', 8000);
      }
    } catch (e) {
      showToast(`Error initiating OAuth flow: ${e.message}`, 'error');
    } finally {
      if (btn) setTimeout(() => { btn.disabled = false; }, 1000);
    }
  }

  // =========================================================================
  // Live Proxy Event Logs & Filters
  // =========================================================================

  function appendLog(event) {
    if (!event) return;
    allEvents.push(event);

    // Keep buffer capped at 300 events
    if (allEvents.length > 300) {
      allEvents.shift();
    }

    renderLogEntry(event);
  }

  function renderLogEntry(event) {
    if (!logsContainer) return;

    if (!matchesFilter(event, currentLogFilter)) {
      return;
    }

    const entry = document.createElement('div');
    entry.className = 'log-entry';

    let pillClass = 'pill-info';
    let pillText = (event.type || 'INFO').toUpperCase();

    switch (event.type) {
      case 'failover_429':
        pillClass = 'pill-failover';
        pillText = 'FAILOVER 429';
        break;
      case 'quota_exhausted':
        pillClass = 'pill-exhausted';
        pillText = 'QUOTA EXHAUSTED';
        break;
      case 'quota_restored':
        pillClass = 'pill-restored';
        pillText = 'QUOTA RESTORED';
        break;
      case 'account_switched':
        pillClass = 'pill-switched';
        pillText = 'SWITCH';
        break;
      case 'tokens_captured':
        pillClass = 'pill-tokens';
        pillText = 'TOKENS';
        break;
      case 'model_fallback':
        pillClass = 'pill-fallback';
        pillText = 'MODEL FALLBACK';
        break;
      case 'error':
        pillClass = 'pill-error';
        pillText = 'ERROR';
        break;
    }

    const timeStr = formatTimeOnly(event.timestamp);
    const accPart = event.account_id ? `<span class="log-account">[${escapeHtml(event.account_id.slice(0, 8))}]</span> ` : '';
    const formattedMsg = formatLogMessage(event.message || '');

    entry.innerHTML = `
      <span class="log-time">${escapeHtml(timeStr)}</span>
      <span class="log-pill ${pillClass}">${escapeHtml(pillText)}</span>
      <span class="log-message">${accPart}${formattedMsg}</span>
    `;

    logsContainer.appendChild(entry);

    // Trim DOM elements to 200 max
    while (logsContainer.children.length > 200) {
      logsContainer.removeChild(logsContainer.firstChild);
    }

    // Auto-scroll if user has not scrolled up
    if (!isUserScrolledUp) {
      logsContainer.scrollTop = logsContainer.scrollHeight;
    }
  }

  function matchesFilter(event, filter) {
    if (filter === 'all') return true;
    if (filter === 'failover' && (event.type === 'failover_429' || event.type === 'model_fallback')) return true;
    if (filter === 'quota' && (event.type === 'quota_exhausted' || event.type === 'quota_restored')) return true;
    if (filter === 'tokens' && event.type === 'tokens_captured') return true;
    if (filter === 'error' && event.type === 'error') return true;
    return false;
  }

  function reapplyLogFilter() {
    if (!logsContainer) return;
    logsContainer.innerHTML = '';
    allEvents.forEach(evt => renderLogEntry(evt));
    logsContainer.scrollTop = logsContainer.scrollHeight;
  }

  // =========================================================================
  // Server-Sent Events (SSE) Real-Time Stream
  // =========================================================================

  function setupSSE() {
    if (eventSource) {
      eventSource.close();
    }

    const badge = document.getElementById('connection-badge');
    const badgeText = document.getElementById('connection-status-text');
    const badgeDot = document.getElementById('connection-dot');

    try {
      eventSource = new EventSource('/api/events');

      eventSource.onopen = function () {
        if (badge && badgeText && badgeDot) {
          badge.className = 'badge badge-success';
          badgeDot.className = 'status-dot status-dot-success';
          badgeText.textContent = 'Connected';
        }
      };

      eventSource.onerror = function () {
        if (badge && badgeText && badgeDot) {
          badge.className = 'badge badge-warning';
          badgeDot.className = 'status-dot status-dot-warning';
          badgeText.textContent = 'Reconnecting...';
        }
      };

      eventSource.onmessage = function (e) {
        try {
          const event = JSON.parse(e.data);
          appendLog(event);

          if (
            event.type === 'account_switched' ||
            event.type === 'failover_429' ||
            event.type === 'quota_restored' ||
            event.type === 'quota_exhausted'
          ) {
            fetchStatus();
            fetchAccounts();
          } else if (event.type === 'tokens_captured') {
            fetchMetrics();
          } else if (event.type === 'model_fallback') {
            fetchConfig();
          }
        } catch (err) {
          console.warn('Failed to parse SSE payload:', err);
        }
      };
    } catch (e) {
      console.warn('EventSource initialization failed:', e);
    }
  }

  // =========================================================================
  // Event Listeners & Boot
  // =========================================================================

  function initListeners() {
    // Cross-card Pool Circuit Hover Sync
    if (accountsGrid) {
      accountsGrid.addEventListener('mouseover', (e) => {
        const bucket = e.target.closest('.quota-group[data-in-pool="true"]');
        if (!bucket) return;
        const catKey = bucket.dataset.categoryGroup;
        if (!catKey) return;
        accountsGrid.querySelectorAll(`.quota-group[data-category-group="${catKey}"][data-in-pool="true"]`).forEach(el => {
          el.classList.add('pool-circuit-active');
        });
        const svg = document.getElementById('pool-circuit-canvas');
        if (svg) {
          svg.querySelectorAll(`.circuit-path[data-category="${catKey}"], .circuit-dot[data-category="${catKey}"]`).forEach(el => {
            el.classList.add('active');
          });
        }
      });

      accountsGrid.addEventListener('mouseout', (e) => {
        const bucket = e.target.closest('.quota-group[data-in-pool="true"]');
        if (!bucket) return;
        if (e.relatedTarget && bucket.contains(e.relatedTarget)) return;
        const catKey = bucket.dataset.categoryGroup;
        if (!catKey) return;
        accountsGrid.querySelectorAll(`.quota-group[data-category-group="${catKey}"][data-in-pool="true"]`).forEach(el => {
          el.classList.remove('pool-circuit-active');
        });
        const svg = document.getElementById('pool-circuit-canvas');
        if (svg) {
          svg.querySelectorAll(`.circuit-path[data-category="${catKey}"], .circuit-dot[data-category="${catKey}"]`).forEach(el => {
            el.classList.remove('active');
          });
        }
      });
    }

    window.addEventListener('resize', () => {
      requestAnimationFrame(() => drawPoolCircuits());
    });

    // Fallback modal setup
    setupFallbackModalEvents();

    // Refresh models button with account refetch
    if (btnRefreshModels) {
      btnRefreshModels.addEventListener('click', async () => {
        await fetchModels();
        await fetchAccounts();
        showToast('AI models refreshed from Antigravity', 'info', 2000);
      });
    }

    // Sync / Refresh Quotas button
    const btnRefresh = document.getElementById('btn-refresh');
    const refreshIcon = document.getElementById('refresh-icon');

    const triggerRefresh = async () => {
      if (refreshIcon) {
        refreshIcon.style.transition = 'transform 0.5s ease-in-out';
        refreshIcon.style.transform = 'rotate(360deg)';
      }
      try {
        await fetch('/api/quota/refresh', { method: 'POST' });
        showToast('Quota refresh complete', 'info', 2000);
      } catch (e) {
        showToast(`Quota refresh failed: ${e.message}`, 'error');
      } finally {
        setTimeout(() => {
          if (refreshIcon) {
            refreshIcon.style.transition = 'none';
            refreshIcon.style.transform = 'rotate(0deg)';
          }
        }, 500);
      }
      await Promise.all([fetchStatus(), fetchAccounts(), fetchMetrics(), fetchModels()]);
    };

    if (btnRefresh) {
      btnRefresh.addEventListener('click', triggerRefresh);
    }

    // Keyboard shortcut: Press R to refresh quotas
    window.addEventListener('keydown', (e) => {
      if ((e.key === 'r' || e.key === 'R') && !e.ctrlKey && !e.metaKey && !e.altKey && !['INPUT', 'TEXTAREA'].includes(document.activeElement?.tagName)) {
        e.preventDefault();
        triggerRefresh();
      }
    });

    // Add account button
    const btnAddAccount = document.getElementById('btn-add-account');
    if (btnAddAccount) {
      btnAddAccount.addEventListener('click', handleAddAccount);
    }

    // Listen for OAuth postMessage events from callback window
    window.addEventListener('message', (event) => {
      if (event.data && event.data.type === 'oauth_success') {
        showToast(`Account ${event.data.email || ''} connected successfully!`, 'success');
        fetchAccounts();
        fetchStatus();
      } else if (event.data && event.data.type === 'oauth_error') {
        showToast(`OAuth authorization failed: ${event.data.error}`, 'error');
      }
    });

    // Clear logs button
    const btnClearLogs = document.getElementById('btn-clear-logs');
    if (btnClearLogs && logsContainer) {
      btnClearLogs.addEventListener('click', () => {
        allEvents = [];
        logsContainer.innerHTML = `
          <div class="log-entry">
            <span class="log-time">--:--:--</span>
            <span class="log-pill pill-info">SYSTEM</span>
            <span class="log-message text-muted">Logs cleared by user.</span>
          </div>
        `;
        showToast('Console logs cleared', 'info', 2000);
      });
    }

    // Copy all logs button
    const btnCopyLogs = document.getElementById('btn-copy-logs');
    if (btnCopyLogs) {
      btnCopyLogs.addEventListener('click', () => {
        if (allEvents.length === 0) {
          showToast('No logs to copy', 'info', 2000);
          return;
        }
        const textLines = allEvents.map(evt => {
          const t = formatTimeOnly(evt.timestamp);
          const type = (evt.type || 'INFO').toUpperCase();
          const acc = evt.account_id ? `[${evt.account_id.slice(0, 8)}] ` : '';
          let msg = evt.message || '';
          if (isPrivacyMode) {
            msg = redactEmails(msg);
          }
          return `[${t}] [${type}] ${acc}${msg}`;
        }).join('\n');
        copyToClipboard(textLines, isPrivacyMode ? 'Redacted proxy logs' : 'Proxy logs');
      });
    }

    // Privacy Mode Toggle Button
    const btnPrivacy = document.getElementById('btn-privacy');
    if (btnPrivacy) {
      btnPrivacy.addEventListener('click', togglePrivacyMode);
    }

    // Global keyboard shortcut: 'P' to toggle Privacy Mode
    document.addEventListener('keydown', (e) => {
      if (e.target && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' || e.target.isContentEditable)) {
        return;
      }
      if ((e.key === 'p' || e.key === 'P') && !e.ctrlKey && !e.altKey && !e.metaKey) {
        e.preventDefault();
        togglePrivacyMode();
      }
    });

    // Log scroll detection (pause auto-scroll when user inspects history)
    if (logsContainer) {
      logsContainer.addEventListener('scroll', () => {
        const threshold = 30;
        const atBottom = logsContainer.scrollHeight - logsContainer.scrollTop - logsContainer.clientHeight < threshold;
        isUserScrolledUp = !atBottom;
      });
    }

    // Log filter buttons
    const filterButtons = document.querySelectorAll('.filter-btn');
    filterButtons.forEach(btn => {
      btn.addEventListener('click', () => {
        filterButtons.forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        currentLogFilter = btn.getAttribute('data-filter') || 'all';
        reapplyLogFilter();
      });
    });

    // Period selector buttons
    const periodButtons = document.querySelectorAll('.metrics-tabs .tab-btn');
    periodButtons.forEach(btn => {
      btn.addEventListener('click', () => {
        periodButtons.forEach(b => {
          b.classList.remove('active');
          b.setAttribute('aria-selected', 'false');
        });
        btn.classList.add('active');
        btn.setAttribute('aria-selected', 'true');
        currentPeriod = btn.getAttribute('data-period') || 'lifetime';
        fetchMetrics();
      });
    });
  }

  // Application initialization
  document.addEventListener('DOMContentLoaded', () => {
    updatePrivacyUI();
    initListeners();
    fetchStatus();
    fetchAccounts();
    fetchMetrics();
    fetchConfig();
    fetchModels();
    setupSSE();

    // Trigger one initial quota synchronization pass
    fetch('/api/quota/refresh', { method: 'POST' })
      .then(() => {
        fetchStatus();
        fetchAccounts();
      })
      .catch(() => {});

    // Live local ticker for quota cooldowns (ticks every 1s, zero network cost)
    setInterval(updateCooldownTimers, 1000);

    // Fallback sync polling every 30 seconds
    setInterval(() => {
      fetchStatus();
      fetchAccounts();
      fetchMetrics();
    }, 30000);
  });
})();
