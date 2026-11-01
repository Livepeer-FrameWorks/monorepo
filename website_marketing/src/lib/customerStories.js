// Stories stay unlisted: nothing links to them, they are left out of the sitemap,
// and each page is served noindex. Share a story by its direct URL.
//
// Story shape:
//   slug, customer, headline, summary: strings
//   situation, approach: arrays of paragraphs
//   setup: array of { title, description }
//   results: array of { value, label }
//   quote: optional { text, author, role }
export const customerStories = [];

export const customerStoryPath = (story) => `/customers/${story.slug}`;

export const findCustomerStory = (slug, stories = customerStories) =>
  stories.find((story) => story.slug === slug);
