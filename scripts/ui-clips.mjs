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
      const audio = li.querySelector('audio');
      return {
        when: [...(li.querySelectorAll('.prov span') ?? [])].map((n) => n.textContent.trim()).filter(Boolean),
        src: audio?.getAttribute('src'),
        preload: audio?.getAttribute('preload'),
        controls: audio?.hasAttribute('controls') ?? false,
      };
    `,
      60,
      100,
    );
    checks.check(
      'the viewer shows the clip with a player pointed at its file',
      !!clip?.controls && (clip?.src ?? '').startsWith('/api/assets/'),
      JSON.stringify(clip)
    );

    // A source is a hundred and fifty of these. Fetching every one to draw a list of play
    // buttons would read the whole recording off the disk to show its table of contents.
    checks.check(
      'the player waits for the reader to press play',
      clip?.preload === 'none',
      `preload=${clip?.preload}`
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
  }
);

// report() is what makes a FAIL line cost anything: the browser returning cleanly
// says nothing about the checks the page just went through.
process.exit(checks.report() || code);
