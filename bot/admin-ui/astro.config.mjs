// @ts-check
import { defineConfig } from 'astro/config';

// Статика с базой /admin: страницы отдаёт Go из вшитых файлов, поэтому
// никакого SSR и никакого Node в проде. Данные страницы забирают с
// /admin/api сами — так вёрстка правится без пересборки Go.
export default defineConfig({
  base: '/admin',
  trailingSlash: 'always',
  build: { format: 'directory' },
  devToolbar: { enabled: false },
});
