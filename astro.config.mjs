import { defineConfig } from 'astro/config';

// https://astro.build/config
export default defineConfig({
  // Поменяй на свой адрес.
  // - Корневой (ник.github.io или свой домен): site указываешь, base НЕ нужен.
  // - Проектный (ник.github.io/имя-репо): раскомментируй base.
  site: 'https://alexeymuzyka.com',
  base: '/',
});
