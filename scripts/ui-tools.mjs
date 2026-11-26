// The sentence a missing external program turns into, on the two machines that mean different
// things by it. The fixture is built by `make ui-tools`, not shared: it needs a libris whose
// PATH holds no poppler and no ffmpeg — so a PDF really cannot be read and a recording cannot
// be sliced — and whose folder beside the executable does or does not contain a `tools`
// folder. Those two facts together are the whole difference between "install it" and "the
// folder came with libris", and no other harness can make them both true.
//
// Two kinds of file, because the sentence is per program and the two come off different
// stages: a PDF names pdftotext and pdftoppm, a recording names ffmpeg. A machine whose tools
// folder got left behind when libris was moved is told about both, in the same shape.
//
// EXPECT=bundle asserts the second sentence; EXPECT=install the first.
import { mkdtempSync } from "node:fs";
import { Checks, withBrowser, UPLOAD_FIXTURE, AUDIO_FIXTURE } from "./cdp.mjs";

const app = process.env.APP ?? "http://127.0.0.1:8080/";
const expect = process.env.EXPECT ?? "bundle";
const cdpPort = Number(process.env.CDP_PORT ?? 9331);
const checks = new Checks();

const code = await withBrowser(
  { port: cdpPort, app, profile: mkdtempSync("/tmp/libris-tools-chrome-") },
  async (ui) => {
    await ui.reload(app);
    await ui.setViewport(1400, 900);
    await ui.settle(1500);

    // A notebook to upload into, because the row and its finding live in one.
    await ui.click("New notebook");
    await ui.eval(`
      const el = document.querySelector('dialog.new-notebook input');
      el.value = 'No Poppler';
      el.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    `);
    await ui.click("Create");
    await ui.settle(600);

    // Two files in one drop. Not a shortcut: a queue that drains only its first entry looks
    // fine on a single upload, and this is the check that would notice.
    await ui.setFiles("input[type=file]", [UPLOAD_FIXTURE, AUDIO_FIXTURE]);

    const rowFor = (name) =>
      ui.until(
        `
        const row = [...document.querySelectorAll('.source')].find((r) =>
          r.querySelector('.name')?.textContent.trim() === ${JSON.stringify(name)});
        if (!row) return null;
        const badge = row.querySelector('.badge')?.textContent.trim();
        if (badge === 'processing' || !badge) return null;
        return {
          status: badge,
          detail: row.querySelector('.note')?.textContent.replace(/\\s+/g, ' ').trim(),
          findings: [...row.querySelectorAll('.findings')].map((n) =>
            n.textContent.replace(/\\s+/g, ' ').trim()
          ),
        };
      `,
        300,
        100,
      );

    const done = await rowFor("two-page.pdf");

    checks.check(
      "a PDF with no way to read it is unsupported, not empty",
      done?.status === "unsupported",
      `status=${done?.status}`,
    );
    checks.check(
      "the row blames the machine rather than the file",
      /no PDF tools/i.test(done?.detail ?? ""),
      done?.detail,
    );

    const said = done?.findings.join(" | ") ?? "";
    checks.check("both programs are named", /pdftotext/.test(said) && /pdftoppm/.test(said), said);

    // The recording goes the same way: unsupported rather than empty, the machine blamed
    // rather than the file, and ffmpeg named out loud.
    const clip = await rowFor("one-second.wav");
    checks.check(
      "a recording with no way to slice it is unsupported, not empty",
      clip?.status === "unsupported",
      `status=${clip?.status}`,
    );
    checks.check(
      "and that row blames the machine too",
      /no ffmpeg/i.test(clip?.detail ?? ""),
      clip?.detail,
    );
    const saidFFmpeg = clip?.findings.join(" | ") ?? "";
    checks.check("the recording names ffmpeg", /ffmpeg/.test(saidFFmpeg), saidFFmpeg);

    // Which sentence is right is a fact about the machine, not about the file, so a
    // recording's row that contradicts the PDF's row is a bug whichever of them is wrong.
    const saysOrigin = (text) =>
      expect === "bundle"
        ? /missing from the tools folder beside libris/.test(text) &&
          /move that folder with it/.test(text)
        : /not installed here/.test(text) && /Install it, then run this file again/.test(text);

    if (expect === "bundle") {
      checks.check(
        "a download is told about the folder, not told to install",
        saysOrigin(said),
        said,
      );
      checks.check("and is never told to install it", !/[Ii]nstall it/.test(said), said);
    } else {
      checks.check(
        "a machine that installs its own programs is told to install it",
        saysOrigin(said),
        said,
      );
      checks.check("and is not sent looking for a folder it never had", !/tools folder/.test(said), said);
    }
    checks.check(
      `the recording's row says the same thing about this machine (${expect})`,
      saysOrigin(saidFFmpeg),
      saidFFmpeg,
    );
  },
);

// report() is what makes a FAIL line cost anything: the browser returning cleanly
// says nothing about the checks the page just went through.
process.exit(checks.report() || code);
