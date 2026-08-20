// Кладёт сборку туда, откуда её вшивает Go. Отдельным шагом, а не
// outDir: каталог сначала чистится, иначе файлы прошлой сборки остаются
// лежать рядом и раздаются вместе с новыми.
import { cp, mkdir, readdir, rm, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const dist = join(here, '..', 'dist');
const target = join(here, '..', '..', 'internal', 'admin', 'ui');

for (const entry of await readdir(target).catch(() => [])) {
  if (entry !== '.gitkeep') await rm(join(target, entry), { recursive: true, force: true });
}
await mkdir(target, { recursive: true });
await cp(dist, target, { recursive: true });
await writeFile(
  join(target, '.gitkeep'),
  '# Сюда Astro кладёт собранный интерфейс (bot/admin-ui).\n# Файлы сборки не коммитятся, но каталог нужен: на него ссылается go:embed.\n',
);
console.log('интерфейс админки собран в internal/admin/ui');
