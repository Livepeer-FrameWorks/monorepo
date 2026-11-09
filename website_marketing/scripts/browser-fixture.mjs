async function runAttempt(browser, url, remaining) {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const diagnostics = [];
  let networkChanged = false;
  const recordDiagnostic = (message) => {
    if (diagnostics.length < 20) diagnostics.push(message.slice(0, 500));
  };
  page.on("pageerror", (error) => recordDiagnostic(`pageerror: ${error.message}`));
  page.on("requestfailed", (request) => {
    const error = request.failure()?.errorText;
    recordDiagnostic(`requestfailed: ${error} ${request.url()}`);
    if (
      error === "net::ERR_NETWORK_CHANGED" &&
      ["document", "script"].includes(request.resourceType()) &&
      new URL(request.url()).origin === new URL(url).origin
    ) {
      networkChanged = true;
    }
  });
  let status = "timeout";
  let report = "";
  try {
    await page.goto(url, { timeout: Math.min(30_000, remaining()) });
    const output = page.locator('output[data-status="pass"], output[data-status="fail"]');
    await output.waitFor({ timeout: remaining() });
    status = await output.getAttribute("data-status", { timeout: remaining() });
    report = await output.textContent({ timeout: remaining() });
  } catch (error) {
    const output = page.locator("output");
    report =
      ((await output.count()) > 0 &&
        (await output.textContent({ timeout: remaining() }).catch(() => ""))) ||
      error.message;
  } finally {
    await page.close();
  }
  return { status, report, diagnostics, networkChanged };
}

export async function runBrowserFixture(browser, url, onRetry) {
  const deadline = Date.now() + 60_000;
  const remaining = () => Math.max(1, deadline - Date.now());
  const result = await runAttempt(browser, url, remaining);
  if (result.status === "pass" || !result.networkChanged || Date.now() >= deadline) return result;

  // Chromium can abort local bootstrap modules when the host network changes.
  // Dispose the interrupted document and replay once within the original case deadline.
  onRetry(result);
  return runAttempt(browser, url, remaining);
}
