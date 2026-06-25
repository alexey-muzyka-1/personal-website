import { defineCollection, z } from 'astro:content';

// Книги. Две подборки через поле category:
//   hudozhestvennaya — художественная
//   biznes — бизнес-литература
// Один .md = одна книга. link = ссылка на Википедию (или любую).
const books = defineCollection({
  type: 'content',
  schema: z.object({
    title: z.string(),
    author: z.string(),
    category: z.enum(['hudozhestvennaya', 'biznes']),
    link: z.string().optional(),
    why: z.string().optional(),
    date: z.coerce.date().optional(),
  }),
});

export const collections = { books };
