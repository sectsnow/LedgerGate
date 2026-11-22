const { chromium } = require('/tmp/ledgergate-ui-check/node_modules/playwright-core');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

(async () => {
  const root = __dirname;
  const marks = JSON.parse(fs.readFileSync(path.join(root, 'concepts.json'), 'utf8'));
  assert.equal(marks.length, 10);
  const browser = await chromium.launch({ executablePath: '/root/.cache/ms-playwright/chromium-1255/chrome-linux64/chrome', headless: true, args: ['--no-sandbox', '--allow-file-access-from-files'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1600, height: 1100 }, deviceScaleFactor: 1 });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    for (const theme of ['light', 'dark']) {
      await page.goto('file://' + path.join(root, 'overview-' + theme + '.html'));
      await page.evaluate(() => document.fonts.ready);
      assert.equal(await page.locator('.concept').count(), 10);
      assert.equal(await page.locator('.big-mark svg').count(), 10);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      assert.equal(await page.evaluate(() => { const ids = [...document.querySelectorAll('[id]')].map(el => el.id); return ids.length === new Set(ids).size; }), true);
      await page.screenshot({ path: path.join(root, 'overview-' + theme + '.png'), fullPage: true });
    }
    for (const width of [1024, 736, 390, 320]) {
      await page.setViewportSize({ width, height: 1100 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'overflow at ' + width);
    }
    const renderChecks = [];
    for (const mark of marks) {
      const folder = path.join(root, 'options', mark.id + '-' + mark.slug);
      for (const theme of ['light', 'dark']) {
        const source = fs.readFileSync(path.join(folder, theme + '.svg'), 'utf8');
        const result = await page.evaluate(async encoded => {
          const img = new Image(); img.src = 'data:image/svg+xml;base64,' + encoded; await img.decode();
          const canvas = document.createElement('canvas'); canvas.width = canvas.height = 512;
          const ctx = canvas.getContext('2d'); ctx.drawImage(img, 0, 0, 512, 512);
          const pixels = ctx.getImageData(0, 0, 512, 512).data;
          let visible = 0, transparent = 0;
          for (let i = 3; i < pixels.length; i += 4) { if (pixels[i] > 0) visible++; else transparent++; }
          return { visible, transparent, png: canvas.toDataURL('image/png').split(',')[1] };
        }, Buffer.from(source).toString('base64'));
        assert.ok(result.visible > 5000 && result.transparent > 5000, mark.id + ' alpha');
        fs.writeFileSync(path.join(folder, theme + '.png'), Buffer.from(result.png, 'base64'));
        renderChecks.push({ id: mark.id, theme, visible_pixels: result.visible });
      }
    }
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(root, 'render-checks.json'), JSON.stringify({ options: 10, themes: ['light', 'dark'], png_size: 512, widths_checked: [1600, 1024, 736, 390, 320], browser_errors: errors.length, transparency: renderChecks }, null, 2) + '\n');
    console.log(JSON.stringify({ options: 10, svg_files: 40, transparent_pngs: renderChecks.length, overviews: 2, widths: [1600, 1024, 736, 390, 320], browser_errors: errors.length }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
