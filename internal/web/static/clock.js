// Setting the unit's clock, and the one question to ask before doing it.
//
// Freeway Pi writes its own local time into the unit, so the write is only as
// right as Freeway Pi's timezone — and every freshly flashed box starts on
// Europe/London, whatever country it is in. The server compares the unit's
// clock with the reader's; when the unit agrees with the reader and Freeway Pi
// does not, it answers "ask first" instead of writing. This asks.
//
// Asks rather than refuses: the browser may be on a laptop in another country,
// and the person holding it may know something the three clocks do not.

// readerWall is the reader's local time as digits with no zone — the same
// terms as the unit's clock, which has none either.
function readerWall() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// clockRequest sends a request that changes the unit's clock, asking first if
// the server thinks it would make the clock wrong. It returns the answer, or
// null when the reader chose not to go ahead.
async function clockRequest(path, method, body) {
  const attempt = async (confirmed) => {
    const res = await fetch(path, {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...body, reader_wall: readerWall(), confirmed }),
    });
    const answer = await res.json().catch(() => ({}));
    return { res, answer };
  };

  let { res, answer } = await attempt(false);
  if (res.status === 409 && answer.needs_confirm) {
    const choice = await askAboutZone(answer.error);
    if (choice === 'zone') {
      location.href = 'system.html#clock';
      return null;
    }
    if (choice !== 'anyway') return null;
    ({ res, answer } = await attempt(true));
  }
  if (!res.ok) throw new Error(answer.error || res.statusText);
  return answer;
}

// askAboutZone shows the question and resolves to 'zone', 'anyway' or 'cancel'.
//
// Its own dialog rather than each page's: the settings and programme pages have
// none, and the answer has three choices where a confirmation has two. The
// timezone is the one offered first and focused, because it is almost always
// the right answer.
function askAboutZone(text) {
  return new Promise((resolve) => {
    const dialog = document.createElement('dialog');
    const p = document.createElement('p');
    p.textContent = text;
    const buttons = document.createElement('div');
    buttons.className = 'dialog-buttons';

    const cancel = document.createElement('button');
    cancel.className = 'ghost';
    cancel.textContent = t('ui.cancel');
    const anyway = document.createElement('button');
    anyway.className = 'danger';
    anyway.textContent = t('ui.clock.anyway');
    const zone = document.createElement('button');
    zone.className = 'primary';
    zone.textContent = t('ui.clock.gozone');
    buttons.append(cancel, anyway, zone);
    dialog.append(p, buttons);
    document.body.append(dialog);

    let answered = false;
    const finish = (choice) => {
      if (answered) return;
      answered = true;
      dialog.close();
      dialog.remove();
      resolve(choice);
    };
    cancel.onclick = () => finish('cancel');
    anyway.onclick = () => finish('anyway');
    zone.onclick = () => finish('zone');
    // Escape, or anything else that closes it, is a no.
    dialog.oncancel = () => finish('cancel');
    dialog.onclose = () => finish('cancel');

    dialog.showModal();
    zone.focus();
  });
}

// wallDigits reads the date and time digits of a timestamp and ignores its
// zone, giving a Date that shows those same digits here.
//
// Two clocks need it. The unit's has no zone at all; the timestamp carries
// Freeway Pi's offset only because that is how the server writes times. And
// Freeway Pi's own is worth showing in its own zone, not converted to this
// browser's — converting was how a box on the wrong timezone looked right.
function wallDigits(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})/.exec(iso || '');
  if (!m || m[1] === '0001') return null;
  return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]);
}

// zoneOf is the offset a timestamp was written with, as "UTC+01:00".
function zoneOf(iso) {
  const m = /([+-]\d{2}:\d{2}|Z)$/.exec(iso || '');
  if (!m) return '';
  return m[1] === 'Z' ? 'UTC' : `UTC${m[1]}`;
}
