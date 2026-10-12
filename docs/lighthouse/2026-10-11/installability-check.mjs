import { chromium } from 'playwright';
const ck = process.env.CK;
const pages = [['home','/'],['plan','/plan'],['discover','/discover'],['dinner','/dinner/01M4PKT9QYA6C4JCF67XE7RA6R']];
const browser = await chromium.launch({ executablePath: '/usr/bin/google-chrome', args: ['--no-sandbox'] });
const ctx = await browser.newContext({ userAgent: 'Mozilla/5.0 (Linux; Android 11) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Mobile Safari/537.36' });
await ctx.addCookies([{ name: '__Host-mealquest_session', value: ck, url: 'https://mealquest.cubecraftlabs.com/' }]);
let fail = 0;
for (const [name, path] of pages) {
  const page = await ctx.newPage();
  page.on('console', m => { if (/sw|service|error/i.test(m.text())) console.log('  [console]', m.text().slice(0,120)); });
  page.on('pageerror', e => console.log('  [pageerror]', String(e).slice(0,120)));
  await page.goto('https://mealquest.cubecraftlabs.com' + path, { waitUntil: 'networkidle', timeout: 30000 });
  const href = await page.evaluate(() => document.querySelector('link[rel="manifest"]')?.href || null);
  const sw = await page.evaluate(async () => {
    try { await navigator.serviceWorker.ready; } catch (e) { return { err: String(e) }; }
    const regs = await navigator.serviceWorker.getRegistrations();
    return { registrations: regs.length, scope: regs[0]?.scope || null, state: regs[0]?.active?.state || null, controlling: !!navigator.serviceWorker.controller };
  });
  const manifest = href ? await page.evaluate(async (h) => { const r = await fetch(h); return { status: r.status, body: await r.json() }; }, href) : { status: 'no link' };
  const m = manifest.body || {};
  const icons = (m.icons || []).map(i => i.sizes + (i.purpose ? ':' + i.purpose : ''));
  const okSw = sw.registrations > 0;
  const okIcons = icons.some(s => s.startsWith('192')) && icons.some(s => s.startsWith('512')) && icons.some(s => s.includes('maskable'));
  const okManifest = m.name === 'MealQuest' && m.display === 'standalone' && !!m.start_url && okIcons;
  const pass = okSw && okManifest;
  if (!pass) fail++;
  console.log(`${name}: url=${page.url().replace('https://mealquest.cubecraftlabs.com','')} manifest=${manifest.status} name=${m.name} display=${m.display} icons=[${icons.join(',')}] SW=${JSON.stringify(sw)} => ${pass ? 'INSTALLABLE' : 'NOT INSTALLABLE'}`);
  await page.close();
}
await browser.close();
process.exit(fail ? 1 : 0);
