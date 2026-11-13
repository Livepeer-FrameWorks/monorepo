export async function createPlaybackBrowserPage(browser) {
  // A .localhost page is trustworthy and matches the stack's HTTP transport.
  const origin = "http://customer-player.localhost";
  const context = await browser.newContext();
  // Keep private-network permission scoped to the synthetic player origin.
  await context.grantPermissions(["local-network-access"], { origin });
  const page = await context.newPage();
  await page.route(`${origin}/`, (route) =>
    route.fulfill({
      contentType: "text/html",
      body: '<!doctype html><video id="video" muted autoplay playsinline></video>',
    })
  );
  await page.goto(`${origin}/`);
  return { context, page, origin };
}
