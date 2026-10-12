import { chromium, devices } from 'playwright';
const ck = process.env.CK;
if (!ck) { console.error('CK (session cookie) is not set'); process.exit(2); }
const origin = 'https://mealquest.cubecraftlabs.com';
const pages = [['home','/'],['plan','/plan'],['discover','/discover'],['dinner','/dinner/01M4PKT9QYA6C4JCF67XE7RA6R']];
const SW_READY_TIMEOUT_MS = 10000;
const browser = await chromium.launch({ executablePath: '/usr/bin/google-chrome', args: ['--no-sandbox'] });
// Mobile emulation: Pixel 5 viewport, device scale, touch, isMobile and user agent.
const ctx = await browser.newContext({ ...devices['Pixel 5'] });
await ctx.addCookies([{ name: '__Host-mealquest_session', value: ck, url: origin + '/' }]);
let fail = 0;
for (const [name, path] of pages) {
  const page = await ctx.newPage();
  page.on('console', m => { if (/sw|service|error/i.test(m.text())) console.log('  [console]', m.text().slice(0,120)); });
  page.on('pageerror', e => console.log('  [pageerror]', String(e).slice(0,120)));
  await page.goto(origin + path, { waitUntil: 'networkidle', timeout: 30000 });
  const href = await page.evaluate(() => document.querySelector('link[rel="manifest"]')?.href || null);
  const sw = await page.evaluate(async (ms) => {
    const ready = await Promise.race([
      navigator.serviceWorker.ready.then(() => 'ready', e => 'error: ' + String(e)),
      new Promise(r => setTimeout(() => r('timeout after ' + ms + ' ms'), ms)),
    ]);
    const regs = await navigator.serviceWorker.getRegistrations();
    return { ready, registrations: regs.length, state: regs[0]?.active?.state || null };
  }, SW_READY_TIMEOUT_MS);
  const manifest = href
    ? await page.evaluate(async (h) => { const r = await fetch(h); return { status: r.status, body: r.ok ? await r.json() : {} }; }, href)
    : { status: 'no manifest link', body: {} };
  const m = manifest.body;
  const icons = m.icons || [];
  const hasSize = (s) => icons.some(i => (i.sizes || '').split(/\s+/).includes(s));
  const checks = [
    ['manifest HTTP 200', manifest.status === 200, manifest.status],
    ['manifest name "MealQuest"', m.name === 'MealQuest', m.name],
    ['manifest display "standalone"', m.display === 'standalone', m.display],
    ['manifest start_url present', !!m.start_url, m.start_url],
    ['icon 192x192', hasSize('192x192'), icons.map(i => i.sizes).join(',')],
    ['icon 512x512', hasSize('512x512'), icons.map(i => i.sizes).join(',')],
    ['icon purpose maskable', icons.some(i => (i.purpose || '').split(/\s+/).includes('maskable')), icons.map(i => i.purpose || 'any').join(',')],
    ['service worker ready', sw.ready === 'ready', sw.ready],
    ['service worker registered', sw.registrations > 0, sw.registrations],
    ['service worker activated', sw.state === 'activated', sw.state],
  ];
  console.log(`${name} (${path}) url=${page.url().replace(origin, '')}:`);
  for (const [label, ok, actual] of checks) console.log(`  ${ok ? 'PASS' : 'FAIL'} ${label} (got ${JSON.stringify(actual ?? null)})`);
  const pass = checks.every(([, ok]) => ok);
  if (!pass) fail++;
  console.log(`  => ${pass ? 'INSTALLABLE' : 'NOT INSTALLABLE'}`);
  await page.close();
}
await browser.close();
console.log(fail ? `FAILED on ${fail} of ${pages.length} pages` : `PASS on all ${pages.length} pages`);
process.exit(fail ? 1 : 0);
