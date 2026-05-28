// Prefer system Chromium when available (CI containers, Nix shells);
// fall back to Puppeteer's bundled download for local devs.
const { existsSync } = require('node:fs');

const candidates = [
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
  '/usr/bin/google-chrome',
  '/usr/bin/google-chrome-stable',
];

const found = candidates.find(existsSync);

module.exports = found
  ? { executablePath: found, skipDownload: true }
  : {};
