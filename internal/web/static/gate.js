'use strict';

// The PIN gate, shared by every page that has one.
//
// It used to live only in settings.js, and the other three pages carried a
// cut-down copy that could log in but never set a PIN. So a freshly flashed box
// answered the System page with "auth: no PIN has been set" and no way to set
// one — the feature existed on exactly one page, and nothing said which.
//
// One implementation, four pages. A gate that can only unlock is a gate that
// somebody has to already be through.

// gateSetUp wires the form on this page.
//
//   help      message id for the line shown when a PIN already exists
//   unlocked  called once the session is open, to draw the page
function gateSetUp(opts) {
  const form = document.getElementById('gate-form');
  if (!form) return;
  form.addEventListener('submit', (e) => gateSubmit(e, opts));
}

// gateShow puts the gate in front of the page, worded for whichever of the two
// situations this is: no PIN yet, or one that has to be typed.
function gateShow(status, opts) {
  const set = (id, text) => {
    const n = document.getElementById(id);
    if (n && text) n.textContent = text;
  };
  const pin = document.getElementById('pin');

  if (!status.configured) {
    set('gate-label', t('ui.pin.none'));
    set('gate-help', t('ui.pin.first'));
    set('gate-submit', t('ui.pin.set'));
    set('gate-note', t('ui.pin.private'));
    if (pin) pin.setAttribute('autocomplete', 'new-password');
  } else {
    set('gate-label', t('ui.locked'));
    set('gate-help', t(opts.help));
    set('gate-submit', t('ui.unlock'));
    set('gate-note', t('ui.pin.remember'));
    if (pin) pin.setAttribute('autocomplete', 'current-password');
  }
}

async function gateSubmit(event, opts) {
  event.preventDefault();
  const input = document.getElementById('pin');
  const pin = input.value;
  if (!pin) return;

  const button = document.getElementById('gate-submit');
  const original = button.textContent;
  // Deriving the key takes about a second of pure JavaScript on a phone, and it
  // blocks the page while it runs. Saying so beats looking broken.
  button.disabled = true;
  button.textContent = t('ui.computing');
  if (typeof banner === 'function') banner('', '');

  // Yield twice, so the browser paints the new label before the main thread is
  // tied up.
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));

  try {
    const params = await (await fetch('api/auth/challenge')).json();
    const key = deriveKey(pin, params.salt, params.iterations);

    if (!params.configured) {
      // At setup the server has nothing to compare against, so it is given the
      // derived key once. After that only proofs are ever sent.
      const res = await fetch('api/auth/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ nonce: params.nonce, key: toBase64(key) }),
      });
      if (!res.ok) throw new Error((await res.json()).error || res.statusText);
      // Setting a PIN does not then ask for it.
      await gateLogIn(pin);
    } else {
      await gateLogIn(pin, params, key);
    }
    input.value = '';
    await opts.unlocked();
  } catch (err) {
    if (typeof banner === 'function') banner(err.message, '');
  } finally {
    button.disabled = false;
    button.textContent = original;
  }
}

// gateLogIn exchanges a proof for a session. The challenge is reused when it
// has not been spent, because each one costs a PBKDF2 derivation.
async function gateLogIn(pin, params, key) {
  const p = params || await (await fetch('api/auth/challenge')).json();
  const k = key || deriveKey(pin, p.salt, p.iterations);
  const res = await fetch('api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ nonce: p.nonce, proof: proveNonce(k, p.nonce) }),
  });
  if (!res.ok) throw new Error((await res.json()).error || t('ui.pin.wrong'));
}
