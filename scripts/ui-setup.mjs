// The first-run gate: what a machine with nothing installed sees, and — with READY=1 — what a
// ready one does not. Needs a real binary on a real data directory, so it has its own fixture;
// `make ui`'s is deliberately external-mode, where there is nothing here to set up.
import { mkdtempSync } from "node:fs";
import { Checks, withBrowser } from "./cdp.mjs";

const app = process.env.APP ?? "http://127.0.0.1:8080/";
const ready = process.env.READY === "1";
const cdpPort = Number(process.env.CDP_PORT ?? 9329);
const checks = new Checks();

const code = await withBrowser(
  { port: cdpPort, app, profile: mkdtempSync("/tmp/libris-setup-chrome-") },
  async (ui) => {
    await ui.reload(app);
    await ui.setViewport(1400, 900);
    await ui.settle(2000);

    const seen = await ui.eval(`
      const s = document.querySelector('.setup');
      return {
        wizard: !!s,
        shell: !!document.querySelector('.sidebar'),
        words: s ? s.innerText.includes('Set up Libris') : false,
        rows: s ? s.querySelectorAll('.steps li').length : 0,
        active: s ? s.querySelector('.steps li.active .name')?.textContent ?? '' : '',
        buttons: s ? [...s.querySelectorAll('button')].map((b) => b.textContent.trim()).join('|') : '',
      };
    `);

    if (ready) {
      checks.check("a ready machine goes straight in", !seen.wizard && seen.shell, JSON.stringify(seen));
    } else {
      checks.check("the wizard is what opens", seen.wizard, JSON.stringify(seen));
      checks.check("the app behind it is not mounted", !seen.shell, JSON.stringify(seen));
      checks.check("it says what it is", seen.words);
      checks.check("three steps are listed", seen.rows === 3, String(seen.rows));
      checks.check("it opens on the engine step", seen.active === "Engine", seen.active);
      checks.check("it offers the engine download", /Download llama\.cpp/.test(seen.buttons), seen.buttons);
    }

    checks.check(
      "no page errors",
      ui.pageErrors.length === 0,
      ui.pageErrors.slice(0, 2).join(" | "),
    );
    checks.check(
      "no console errors",
      ui.consoleErrors.length === 0,
      ui.consoleErrors.slice(0, 2).join(" | "),
    );

    return 0;
  },
);

process.exit(checks.report() || code);
