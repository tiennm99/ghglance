// ghglance web UI enhancements. Every page works without JavaScript; this
// adds browser-timezone detection, a live list of the GitHub permissions a
// sign-in or a new token asks for, and job-status polling.
'use strict';

/**
 * Fills a timezone field still at its default with the browser's zone.
 * @param {HTMLInputElement} input
 */
function detectTimezone(input) {
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (tz && (input.value === '' || input.value === 'UTC')) input.value = tz;
  } catch (_) {
    // Older browsers without Intl time zones keep the server default.
  }
}

/**
 * Lists the GitHub scopes a sign-in will request for the ticked options.
 * Mirrors oauthScopes in internal/web/oauth.go.
 * @param {boolean} includePrivate
 * @param {boolean} includeOrgs
 * @returns {string[]}
 */
function oauthScopes(includePrivate, includeOrgs) {
  if (!includePrivate) return ['read:user'];
  return includeOrgs ? ['repo', 'read:user', 'read:org'] : ['repo', 'read:user'];
}

/**
 * Appends scopes to el as a comma-separated list of code elements.
 * @param {HTMLElement} el
 * @param {string[]} scopes
 */
function appendScopes(el, scopes) {
  scopes.forEach((scope, i) => {
    if (i > 0) el.append(', ');
    const code = document.createElement('code');
    code.textContent = scope;
    el.append(code);
  });
}

/**
 * Replaces the sign-in hint's general rule with the exact scopes for the
 * current ticks, and points the "Create a token" link at the same scopes,
 * updated as the ticks change.
 * @param {HTMLFormElement} form
 */
function wireOAuthScopes(form) {
  const hint = /** @type {HTMLElement|null} */ (form.querySelector('[data-oauth-scopes]'));
  const priv = /** @type {HTMLInputElement|null} */ (form.querySelector('input[name="include_private"]'));
  const orgs = /** @type {HTMLInputElement|null} */ (form.querySelector('input[name="include_org_repos"]'));
  const link = /** @type {HTMLAnchorElement|null} */ (form.querySelector('a[data-token-link]'));
  const linkHint = /** @type {HTMLElement|null} */ (form.querySelector('[data-token-scopes]'));
  if (!hint || !priv || !orgs) return;
  const sync = () => {
    const scopes = oauthScopes(priv.checked, orgs.checked);
    const broad = scopes.includes('repo');
    hint.textContent = '';
    hint.append('GitHub will ask for: ');
    appendScopes(hint, scopes);
    hint.append(broad
      ? '. repo is GitHub\'s only private-repository scope and includes write access; '
      : '. Public data only; ');
    hint.append('the token is used for this one generation, then revoked: never stored, never logged.');
    if (link) {
      const next = new URL(link.href);
      next.searchParams.set('scopes', scopes.join(','));
      link.href = next.toString();
    }
    if (linkHint) {
      linkHint.textContent = '';
      linkHint.append('Opens a classic token with ');
      appendScopes(linkHint, scopes);
      linkHint.append(broad
        ? ' already ticked, as private repos are ticked; such a token only works for your own account. '
        : ' already ticked, which covers public data. ');
      linkHint.append('Pick a short expiration, generate it, and paste it here.');
    }
  };
  priv.addEventListener('change', sync);
  orgs.addEventListener('change', sync);
  sync();
}

/**
 * Formats seconds as "1m 05s" for the progress line.
 * @param {number} secs
 * @returns {string}
 */
function formatElapsed(secs) {
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  return m > 0 ? `${m}m ${String(s).padStart(2, '0')}s` : `${s}s`;
}

/**
 * Polls the job-status endpoint and reloads once the job leaves the queue,
 * so the server renders either the finished cards or the failure. The
 * announced line (a live region) changes only with the state or stage, or
 * once when polls start failing; the ticking elapsed time sits outside it
 * so screen readers are not re-read the line every poll.
 * @param {HTMLElement} box
 */
function pollStatus(box) {
  const url = box.dataset.statusUrl;
  const text = document.getElementById('progress-text');
  const clock = document.getElementById('progress-elapsed');
  if (!url || !text) return;
  let delay = 3000;
  let failures = 0;
  const tick = () => {
    fetch(url, { cache: 'no-store', headers: { Accept: 'application/json' } })
      .then((res) => {
        if (!res.ok) throw new Error(`status ${res.status}`);
        return res.json();
      })
      .then((/** @type {{state: string, stage?: string, position?: number, elapsed_seconds?: number}} */ st) => {
        if (st.state !== 'queued' && st.state !== 'running') {
          // Drop ?notice=pending, which no longer describes the page once
          // the job is over; a granted-scope notice still does.
          const next = new URL(window.location.href);
          if (next.searchParams.get('notice') === 'pending') next.searchParams.delete('notice');
          window.location.replace(next.toString());
          return;
        }
        const line = st.state === 'queued'
          ? `Queued${st.position ? `, position ${st.position}` : ''}`
          : `Running: ${st.stage || 'working'}`;
        if (text.textContent !== line) text.textContent = line;
        if (clock) clock.textContent = st.elapsed_seconds ? `Elapsed ${formatElapsed(st.elapsed_seconds)}` : '';
        failures = 0;
        delay = 3000;
        setTimeout(tick, delay);
      })
      .catch(() => {
        // Say so once, after a second failure in a row; the next good poll
        // writes the progress line back.
        failures += 1;
        if (failures === 2) {
          text.textContent = 'Lost contact with the server, retrying…';
          if (clock) clock.textContent = '';
        }
        delay = Math.min(delay * 2, 30000);
        setTimeout(tick, delay);
      });
  };
  setTimeout(tick, delay);
}

document.documentElement.classList.add('js');

document.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('input[data-autotz]').forEach((el) => detectTimezone(/** @type {HTMLInputElement} */ (el)));
  document.querySelectorAll('form.gen').forEach((el) => wireOAuthScopes(/** @type {HTMLFormElement} */ (el)));
  const progress = document.getElementById('progress');
  if (progress) pollStatus(progress);
});
