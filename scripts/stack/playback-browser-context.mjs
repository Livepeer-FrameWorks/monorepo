export async function createPlaybackBrowserPage(browser) {
  const origin = "https://customer-player.frameworks.test";
  const context = await browser.newContext();
  // Stack media endpoints have private addresses; grant only this secure fixture access.
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
