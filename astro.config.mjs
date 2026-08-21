import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

// https://astro.build/config
export default defineConfig({
  // Поменяй на свой адрес.
  // - Корневой (ник.github.io или свой домен): site указываешь, base НЕ нужен.
  // - Проектный (ник.github.io/имя-репо): раскомментируй base.
  site: 'https://alexeymuzyka.com',
  base: '/',
  integrations: [
    // Страницы обложек служебные: с них снимаются картинки для чата.
    // В карту сайта они не идут — предлагать их в поиске незачем.
    sitemap({ filter: (page) => !page.includes('/covers/') }),
  ],

  // Раздел «Агенты» стал «Инструментами»: библиотека шире, чем агенты.
  // Старые адреса не роняем — они уже разошлись по ссылкам.
  redirects: {
    '/agents': '/tools',
    '/agents/content-agent': '/tools/content-agent',
  },
});
