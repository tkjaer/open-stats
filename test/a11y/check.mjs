// Checks the built stats page in Chromium, in light and dark mode: its own
// Content-Security-Policy lets its style apply and blocks nothing else, and
// axe-core finds no accessibility violations with every <details> open.
// test/run.sh runs it in the pinned Playwright container. Test use only.
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { chromium } from "playwright";

const page = process.argv[2];
const axe = readFileSync(createRequire(import.meta.url).resolve("axe-core/axe.min.js"), "utf8");
const browser = await chromium.launch();
let failed = false;
const fail = (msg) => {
  console.log("FAIL " + msg);
  failed = true;
};

for (const colorScheme of ["light", "dark"]) {
  const ctx = await browser.newContext({ colorScheme, javaScriptEnabled: true });
  const p = await ctx.newPage();
  const problems = [];
  p.on("console", (m) => problems.push(m.text()));
  p.on("pageerror", (e) => problems.push(String(e)));
  p.on("request", (r) => {
    if (!r.url().startsWith("file:")) problems.push("request to " + r.url());
  });
  await p.goto("file://" + page);

  const maxWidth = await p.evaluate(() => getComputedStyle(document.body).maxWidth);
  if (maxWidth === "none") fail(`${colorScheme}: the page's style was not applied (blocked by its CSP?)`);

  await p.evaluate(() => document.querySelectorAll("details").forEach((d) => (d.open = true)));
  await p.evaluate(axe);
  const res = await p.evaluate(() => axe.run(document, { resultTypes: ["violations"] }));
  for (const v of res.violations) {
    fail(`${colorScheme}: ${v.id} (${v.impact}): ${v.help}`);
    for (const n of v.nodes.slice(0, 5)) console.log("     " + n.target.join(" ") + ": " + n.failureSummary.replace(/\n/g, " "));
  }
  for (const msg of problems) fail(`${colorScheme}: ${msg}`);
  console.log(`${colorScheme}: axe-core ${res.testEngine.version}, ${res.passes.length} rules passed, ${res.violations.length} violated`);
  await ctx.close();
}
await browser.close();
process.exit(failed ? 1 : 0);
