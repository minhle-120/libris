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
        slots: s ? [...s.querySelectorAll('.devices .slot')].map((e) => e.textContent.trim()).join('|') : '',
      };
    `);

    if (process.env.EXPECT_STEP) {
      // A machine with an engine on disk and no device chosen: the step the last version of
      // this screen could never show, because "the machine reported a device" was true before
      // anyone saw the row.
      checks.check(
        `it stops on the ${process.env.EXPECT_STEP} step`,
        seen.active === process.env.EXPECT_STEP,
        seen.active,
      );
      if (process.env.EXPECT_STEP === 'Inference device') {
        checks.check(
          "and it offers the slots the engine itself reported",
          /(^|\|)(Vulkan|CUDA)\d/.test(seen.slots),
          seen.slots,
        );
        // Whatever the probe reported, the step has to be escapable: a screen that renders a
        // quiet sentence about the CPU and no button is a wall, and it reads as a hang.
        checks.check(
          "and the step offers something to click",
          seen.buttons.length > 0,
          seen.buttons,
        );
        if (process.env.CLICK === '1') {
          // Choosing has to let go of the step, or the fix only moved where the wall is.
          await ui.eval(`document.querySelector('.devices button').click(); return true`);
          await ui.settle(2500);
          const after = await ui.eval(
            `return document.querySelector('.steps li.active .name')?.textContent ?? 'gone'`,
          );
          // Either the next step, or the whole screen gone: on a machine that only lacked a device
          // choice, choosing one is the last thing standing between it and a running Libris.
          checks.check(
            'choosing one lets go of the step',
            after === 'Models' || after === 'gone',
            after,
          );

          if (process.env.SERVE === '1') {
            // The last step there is a server to raise, so ask for it and wait for the gate.
            // This is the way out of the wizard that is not the Skip button, and the shell
            // mounts the moment the gate opens — boot() having stopped at that gate, an app
            // that lands here on "Loading…" is a person who has to reload the page to get in.
            await ui.eval(`
              const b = [...document.querySelectorAll('.setup button')]
                .find((b) => /start/i.test(b.textContent));
              if (b) b.click();
              return !!b;
            `);
            let out = null;
            for (let i = 0; i < 24 && !out; i++) {
              await ui.settle(5000);
              const now = await ui.eval(`
                if (document.querySelector('.setup')) return null;
                const m = document.querySelector('main');
                const t = m ? m.innerText : '';
                return {
                  shell: !!document.querySelector('.shell'),
                  stuck: /Loading/.test(t),
                  text: t.slice(0, 40).replace(/\\n/g, ' · '),
                };
              `);
              if (now) out = now;
            }
            checks.check(
              'and letting go opens an app that is already in',
              !!out && out.shell && !out.stuck && out.text !== '',
              JSON.stringify(out),
            );
          }
        }
      }
    }

    if (ready) {
      checks.check("a ready machine goes straight in", !seen.wizard && seen.shell, JSON.stringify(seen));
    } else {
      checks.check("the wizard is what opens", seen.wizard, JSON.stringify(seen));
      checks.check("the app behind it is not mounted", !seen.shell, JSON.stringify(seen));
      checks.check("it says what it is", seen.words);
      checks.check("three steps are listed", seen.rows === 3, String(seen.rows));
      if (!process.env.EXPECT_STEP) {
        checks.check("it opens on the engine step", seen.active === "Engine", seen.active);
        checks.check("it offers the engine download", /Download llama\.cpp/.test(seen.buttons), seen.buttons);
        // The way past the wall, for a machine that will not run a model server today. It
        // has to open the app, and stay open through a reload — a skip that a refresh
        // takes back is a wall with an extra step in front of it.
        checks.check(
          "it offers a way past the wizard",
          /Skip for now/.test(seen.buttons),
          seen.buttons,
        );
        const skip = async () =>
          await ui.eval(`
            [...document.querySelectorAll('.setup button')]
              .find((b) => b.textContent.includes('Skip'))?.click();
            return true;
          `);
        const where = async () =>
          await ui.eval(
            `const m = document.querySelector('main');
             const t = m ? m.innerText : '';
             return {
               wizard: !!document.querySelector('.setup'),
               shell: !!document.querySelector('.sidebar'),
               stuck: /Loading/.test(t),
             }`,
          );
        await skip();
        await ui.settle(1500);
        const inApp = await where();
        // Not "the wizard is off screen" but "there is an app to use": a shell that mounts
        // onto the boot the gate stopped shows a loading page and nothing else.
        checks.check(
          "skipping walks past it",
          inApp.shell && !inApp.wizard && !inApp.stuck,
          JSON.stringify(inApp),
        );
        await ui.reload(app);
        await ui.settle(1500);
        const reloaded = await where();
        checks.check(
          "and a reload does not put the wizard back",
          reloaded.shell && !reloaded.wizard,
          JSON.stringify(reloaded),
        );
      }
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
