import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';

// Книги. Подборки через поле category:
//   hudozhestvennaya — художественная
//   biznes — бизнес-литература
//   deti — как говорить с детьми и слышать их
// Один .md = одна книга. link = ссылка на Википедию (или любую).
//
// С Astro 6 коллекции обязаны объявлять loader и жить в этом файле,
// а не в src/content/config.ts. Сами .md остались там же, где лежали.
const books = defineCollection({
  loader: glob({ pattern: '**/*.md', base: './src/content/books' }),
  schema: z.object({
    title: z.string(),
    author: z.string(),
    category: z.enum(['hudozhestvennaya', 'biznes', 'deti']),
    link: z.string().optional(),
    why: z.string().optional(),
    date: z.coerce.date().optional(),
  }),
});

export const collections = { books };
