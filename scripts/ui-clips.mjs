// What a recording looks like once it is stored. The other harnesses prove a file reaches
// "ready"; this one opens the viewer and checks the thing a reader actually does with a clip —
// press play — and the number a reader uses to find the right one.
//
// Both are worth a browser rather than a unit test: the player is an element with a src the
// server has to answer, and a preload attribute that fetches a whole recording when it is
// wrong. Neither is visible from Go.
//
// Requires a running server: `make ui` starts one against a throwaway data directory, with
// the fake embeddings server that accepts the clip.
import { mkdtempSync } from 'node:fs';
import { Checks, withBrowser, AUDIO_FIXTURE } from './cdp.mjs';

const app = process.env.APP ?? 'http://127.0.0.1:8080/';
const cdpPort = Number(process.env.CDP_PORT ?? 9341);
const checks = new Checks();

// m:ss–m:ss: the shape formatSpan() writes, and the shape the player under the label shows.
const WINDOW = /^\d+:\d\d–\d+:\d\d$/;

const code = await withBrowser(
  { port: cdpPort, app, profile: mkdtempSync('/tmp/libris-clips-chrome-') },
  async (ui) => {
    await ui.reload(app);
    await ui.setViewport(1400, 900);
    await ui.settle(1500);

    await ui.click('New notebook');
    await ui.eval(`
      const el = document.querySelector('dialog.new-notebook input');
      el.value = 'Recordings';
      el.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    `);
    await ui.click('Create');
    await ui.settle(600);

    await ui.setFiles('input[type=file]', [AUDIO_FIXTURE]);

    const row = await ui.until(
      `
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.querySelector('.name')?.textContent.trim() === 'one-second.wav');
      if (!row) return null;
      const badge = row.querySelector('.badge')?.textContent.trim();
      if (badge === 'processing' || !badge) return null;
      return { status: badge, facts: row.querySelector('.facts')?.textContent.replace(/\\s+/g, ' ').trim() };
    `,
      200,
      100,
    );
    checks.check('a recording takes its row to ready', row?.status === 'ready', JSON.stringify(row));

    // Open the viewer the way a reader does, from the row's own button.
    const opened = await ui.eval(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.querySelector('.name')?.textContent.trim() === 'one-second.wav');
      row?.querySelector('button[title="See how this source was chunked"]')?.click();
      return !!row;
    `);
    checks.check('the row offers the viewer', opened, 'no chunk button on the recording’s row');

    const clip = await ui.until(
      `
      const li = document.querySelector('.clips .clip');
      if (!li) return null;
      return {
        when: [...(li.querySelectorAll('.prov span') ?? [])].map((n) => n.textContent.trim()).filter(Boolean),
        players: document.querySelectorAll('.viewer audio').length,
        inRow: !!li.querySelector('audio'),
      };
    `,
      60,
      100,
    );
    // A source is a hundred and fifty of these. Each used to carry its own <audio>, and
    // building a hundred and fifty media elements to draw a table of contents is what made
    // opening a recording slow. The rows are rows now, and one player serves them.
    checks.check(
      'the clip list is rows, not a media element each',
      clip?.players === 1 && !clip?.inRow,
      JSON.stringify(clip)
    );

    // Pressing a row is the only thing that fetches a clip, and the sound arrives in the one
    // player — aimed at the file of the clip that was pressed.
    await ui.eval(`document.querySelector('.clips .clip .play')?.click(); return true;`);
    const played = await ui.until(
      `
      const el = document.querySelector('.viewer audio');
      if (!el || el.preload !== 'none') return null;
      return {
        src: el.src || el.currentSrc || '',
        paused: el.paused,
        bar: !!document.querySelector('.player.shown'),
        label: document.querySelector('.player .small')?.textContent.replace(/\s+/g, ' ').trim(),
      };
    `,
      60,
      100,
    );
    checks.check(
      'the player waits for the reader, then plays the clip pressed',
      (played?.src ?? '').includes('/api/assets/') && played?.paused === false,
      JSON.stringify(played)
    );
    checks.check(
      'the player says which clip it is playing',
      !!played?.bar && /clip\s+\d/.test(played?.label ?? ''),
      JSON.stringify(played)
    );

    // The label is how a reader finds the sentence they remember hearing. It has to be the
    // same m:ss the player shows a second later.
    checks.check(
      'the clip is labelled with the window of the recording it is',
      (clip?.when ?? []).some((t) => WINDOW.test(t)),
      (clip?.when ?? []).join(' ')
    );

    // And it is named as what it is, so the list is not read as a page rasters table with a
    // strange widget in it.
    const heading = await ui.eval(
      `return [...document.querySelectorAll('.doc-head h3')].map((h) => h.textContent.trim());`
    );
    checks.check(
      'the viewer says these are clips, not pages',
      (heading ?? []).includes('clips'),
      (heading ?? []).join(', ')
    );

    // Opening a recording used to hand the browser a bare audio file, and the browser
    // decided what that meant — play it, or save it. Both are correct answers as far as
    // the browser is concerned, and one of them is not what anyone asked for. The row
    // asks for a page now, and the page has an <audio> in it.
    // Back out of the viewer first: while it is open it stands in the panel's place,
    // and the row whose link is being read is not on the page.
    await ui.eval(`document.querySelector('.viewer [aria-label="Back to sources"]').click(); return true;`);
    await ui.settle(300);
    const link = await ui.eval(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.querySelector('.name')?.textContent.trim() === 'one-second.wav');
      return row?.querySelector('a.iconlink')?.getAttribute('href') ?? null;
    `);
    checks.check(
      'the row opens a recording as a player, not as a file to save',
      /\/api\/sources\/[0-9a-f]{6,}\/player$/.test(link ?? ''),
      () => String(link)
    );

    // The page is a document of its own, so it gets a navigation of its own — which is
    // also exactly what an audio citation does. The clip window arrives in the hash and
    // is read before a byte of sound has been asked for.
    await ui.reload(`${app.replace(/\/$/, '')}${link}#t=0.4`);
    const player = await ui.until(
      `
      const el = document.getElementById('play');
      if (!el || el.readyState === 0) return null;
      return {
        src: el.getAttribute('src'),
        at: Number(el.currentTime.toFixed(2)),
        where: document.getElementById('where')?.textContent.trim(),
        complained: document.getElementById('note')?.hidden !== true,
      };
    `,
      60,
      100,
    );
    checks.check(
      'the player page plays the recording it was asked for',
      /\/original$/.test(player?.src ?? '') && (player?.at ?? 0) >= 0.3,
      () => JSON.stringify(player)
    );
    checks.check(
      'and it says where in the recording it started',
      /from 0:0\d/.test(player?.where ?? ''),
      () => String(player?.where)
    );
    checks.check(
      'a file this browser can decode says nothing about codecs',
      player?.complained === false,
      () => JSON.stringify(player)
    );
  }
);

// report() is what makes a FAIL line cost anything: the browser returning cleanly
// says nothing about the checks the page just went through.
process.exit(checks.report() || code);
