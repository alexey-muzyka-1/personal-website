// Снимает обложки со страниц /covers/<slug>/ и кладёт PNG в public/covers.
//
// Порядок такой: сборка → снимок → PNG лежат в public и попадают в
// следующую сборку. Двухфазность неприятная, но честная: картинки
// коммитятся, поэтому ни Chrome, ни этот скрипт не нужны ни на деплое,
// ни в CI. Заголовки меняются раз в никогда, а сломанный деплой из-за
// отсутствующего браузера — каждый раз.
//
// Рядом с картинками пишется manifest.json с заголовками, из которых они
// сняты. По нему тест видит, что обложка отстала от статьи: молча
// разошедшаяся обложка обещает не то, что откроется.
//
//   node scripts/covers.mjs
//
// Chrome берётся из CHROME или из стандартного места на macOS.

import { execFile } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import { createReadStream, existsSync } from 'node:fs';
import { dirname, extname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const run = promisify(execFile);
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = join(root, 'dist');
const outDir = join(root, 'public', 'covers');

const WIDTH = 1200;
const HEIGHT = 630;
const PORT = 4327;

const CHROME =
  process.env.CHROME ??
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.woff2': 'font/woff2',
};

if (!existsSync(dist)) {
  console.error('Нет dist/. Сначала: npm run build');
  process.exit(1);
}
if (!existsSync(CHROME)) {
  console.error(`Не нашёл Chrome: ${CHROME}\nЗадай путь через CHROME=…`);
  process.exit(1);
}

const slugs = (await readdir(join(dist, 'covers'), { withFileTypes: true }))
  .filter((e) => e.isDirectory())
  .map((e) => e.name)
  .sort();

if (!slugs.length) {
  console.error('В dist/covers пусто: страницы обложек не собрались');
  process.exit(1);
}

// Отдаём собранный сайт с диска: страница тянет шрифты и стили, и
// снимать её через file:// значит снимать без половины оформления.
const server = createServer((req, res) => {
  const path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
  const candidates = [join(dist, path), join(dist, path, 'index.html')];
  const file = candidates.find((c) => existsSync(c) && extname(c));
  if (!file) {
    res.writeHead(404).end('not found');
    return;
  }
  res.writeHead(200, { 'content-type': MIME[extname(file)] ?? 'application/octet-stream' });
  createReadStream(file).pipe(res);
});

await new Promise((ok) => server.listen(PORT, ok));
await mkdir(outDir, { recursive: true });

const manifest = {};
for (const slug of slugs) {
  const out = join(outDir, `${slug}.png`);
  await run(CHROME, [
    '--headless=new',
    '--disable-gpu',
    '--hide-scrollbars',
    // Шрифты приезжают из сети: без ожидания кадр снимается системным
    // шрифтом, и обложка перестаёт быть похожей на сайт.
    '--virtual-time-budget=8000',
    `--window-size=${WIDTH},${HEIGHT}`,
    `--screenshot=${out}`,
    `http://localhost:${PORT}/covers/${slug}/`,
  ]);

  const html = await readFile(join(dist, 'covers', slug, 'index.html'), 'utf8');
  const title = html.match(/<title>([\s\S]*?)<\/title>/)?.[1] ?? '';
  manifest[slug] = title.trim();
  console.log(`covers/${slug}.png ← ${title.trim()}`);
}

await writeFile(join(outDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
server.close();
console.log(`\nГотово: ${slugs.length} обложек в public/covers`);
