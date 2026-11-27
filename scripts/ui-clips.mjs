// What something with a timeline looks like once it is stored: a recording, and a video with
// its two halves. The other harnesses prove a file reaches "ready"; this one opens the viewer
// and checks the thing a reader actually does with a clip — press play — and the number a
// reader uses to find the right one.
//
// Both are worth a browser rather than a unit test: the player is an element with a src the
// server has to answer, and a preload attribute that fetches a whole recording when it is
// wrong. Neither is visible from Go.
//
// Requires a running server: `make ui` starts one against a throwaway data directory, with
// the fake embeddings server that accepts the clip.
import { mkdtempSync } from 'node:fs';
import { Checks, withBrowser, AUDIO_FIXTURE, VIDEO_FIXTURE } from './cdp.mjs';

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

    await ui.setFiles('input[type=file]', [AUDIO_FIXTURE, VIDEO_FIXTURE]);

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

    // ── the same list, for something with pictures ────────────────────────────
    // A video is two documents of one file — its frames, and the soundtrack cut from it —
    // so this is where the two players meet. Same window arithmetic, same single player,
    // and one thing that no unit test can see: a clip of pictures needs an element that
    // shows pictures, and a page of controls under a black box is how that mistake looks.
    const movie = await ui.until(
      `
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.querySelector('.name')?.textContent.trim() === 'four-second.mp4');
      if (!row) return null;
      const badge = row.querySelector('.badge')?.textContent.trim();
      if (badge === 'processing' || !badge) return null;
      return { status: badge, facts: row.querySelector('.facts')?.textContent.replace(/\s+/g, ' ').trim() };
    `,
      200,
      100,
    );
    checks.check('a video takes its row to ready', movie?.status === 'ready', JSON.stringify(movie));

    await ui.eval(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.querySelector('.name')?.textContent.trim() === 'four-second.mp4');
      row?.querySelector('button[title="See how this source was chunked"]')?.click();
      return !!row;
    `);

    // Read by document rather than by row, because the whole point is that the two halves
    // are listed apart and numbered apart while naming the same moments.
    const halves = await ui.until(
      `
      const heads = [...document.querySelectorAll('.doc-head')];
      const sections = heads.map((h) => {
        const list = h.nextElementSibling;
        return {
          title: h.querySelector('h3')?.textContent.trim(),
          count: list?.querySelectorAll('.clip').length ?? 0,
          when: [...(list?.querySelectorAll('.prov span') ?? [])]
            .map((n) => n.textContent.trim()).filter(Boolean),
        };
      });
      const pictures = sections.find((s) => /pictures/.test(s.title ?? ''));
      const sound = sections.find((s) => /soundtrack/.test(s.title ?? ''));
      if (!pictures?.count || !sound?.count) return null;
      return { sections, pictures, sound };
    `,
      200,
      100,
    );
    checks.check(
      'a video is listed as its pictures and its soundtrack, in two documents',
      (halves?.sections ?? []).filter((s) => s.count > 0).length === 2,
      (halves?.sections ?? []).map((s) => `${s.title}(${s.count})`).join(', ')
    );
    checks.check(
      'the same moment is one window in both halves, labelled the way the player reads it',
      (halves?.pictures?.when ?? []).some((t) => WINDOW.test(t)) &&
        halves?.sound?.when?.[0] === halves?.pictures?.when?.[0],
      `pictures ${(halves?.pictures?.when ?? []).join(' ')} · soundtrack ${(halves?.sound?.when ?? []).join(' ')}`
    );

    // Press a clip of the pictures: the file arrives in a video element, and the audio
    // element the recording used is not still on the page underneath it.
    await ui.eval(`document.querySelector('.clips .clip .play')?.click(); return true;`);
    const watched = await ui.until(
      `
      const el = document.querySelector('.viewer video');
      if (!el) return null;
      return {
        src: el.src || el.currentSrc || '',
        paused: el.paused,
        audio: document.querySelectorAll('.viewer audio').length,
        label: document.querySelector('.player .small')?.textContent.replace(/\s+/g, ' ').trim(),
      };
    `,
      60,
      100,
    );
    checks.check(
      'a clip of the pictures plays in a video element, from its own file',
      (watched?.src ?? '').includes('/api/assets/') && watched?.paused === false && watched?.audio === 0,
      JSON.stringify(watched)
    );
    checks.check(
      'and the player says which clip of the video it is playing',
      /clip\s+\d/.test(watched?.label ?? ''),
      JSON.stringify(watched)
    );

    await ui.eval(`document.querySelector('.viewer [aria-label="Back to sources"]').click(); return true;`);
    await ui.settle(300);
    const movieLink = await ui.eval(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.querySelector('.name')?.textContent.trim() === 'four-second.mp4');
      return row?.querySelector('a.iconlink')?.getAttribute('href') ?? null;
    `);
    checks.check(
      'a video row opens a player too, rather than a download',
      /\/api\/sources\/[0-9a-f]{6,}\/player$/.test(movieLink ?? ''),
      () => String(movieLink)
    );

    // The clip form of a citation, on the video's own page: the element is a video, and the
    // window in the hash is what it opens on.
    await ui.reload(`${app.replace(/\/$/, '')}${movieLink}#t=1,3`);
    const stage = await ui.until(
      `
      const el = document.querySelector('video');
      if (!el || el.readyState === 0) return null;
      return {
        src: el.getAttribute('src'),
        at: Number(el.currentTime.toFixed(2)),
        audio: !!document.querySelector('audio'),
        where: document.getElementById('where')?.textContent.trim(),
      };
    `,
      60,
      100,
    );
    checks.check(
      'the page plays a video rather than offering to save it',
      /\/original$/.test(stage?.src ?? '') && stage?.audio === false && (stage?.at ?? 0) >= 0.9,
      () => JSON.stringify(stage)
    );
    checks.check(
      'and it names the cited window of it',
      /0:01–0:03/.test(stage?.where ?? ''),
      () => String(stage?.where)
    );

    // The page is a document of its own, so it gets a navigation of its own — which is
    // also exactly what an audio citation does. The clip window arrives in the hash and
    // is read before a byte of sound has been asked for.
    await ui.reload(`${app.replace(/\/$/, '')}${link}#t=0.4`);
    const player = await ui.until(
      `
      const el = document.querySelector('audio');
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
