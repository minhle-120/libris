// The sentence a missing PDF tool turns into, on the two machines that mean different things
// by it. The fixture is built by `make ui-tools`, not shared: it needs a libris whose PATH
// holds no poppler — so a PDF really cannot be read — and whose folder beside the executable
// does or does not contain a `tools` folder. Those two facts together are the whole difference
// between "install it" and "the folder came with libris", and no other harness can make them
// both true.
//
// EXPECT=bundle asserts the second sentence; EXPECT=install the first.
import { mkdtempSync } from "node:fs";
import { Checks, withBrowser, UPLOAD_FIXTURE } from "./cdp.mjs";

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

    await ui.setFiles("input[type=file]", [UPLOAD_FIXTURE]);

    const done = await ui.until(
      `
      const row = document.querySelector('.source');
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

    if (expect === "bundle") {
      checks.check(
        "a download is told about the folder, not told to install",
        /missing from the tools folder beside libris/.test(said) &&
          /move that folder with it/.test(said),
        said,
      );
      checks.check("and is never told to install it", !/[Ii]nstall it/.test(said), said);
    } else {
      checks.check(
        "a machine that installs its own programs is told to install it",
        /not installed here/.test(said) && /Install it, then run this file again/.test(said),
        said,
      );
      checks.check("and is not sent looking for a folder it never had", !/tools folder/.test(said), said);
    }
  },
);

process.exit(code);
