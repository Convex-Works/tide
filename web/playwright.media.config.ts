import { defineConfig, devices } from '@playwright/test';

const artifactRoot = process.env.KLISI_MEDIA_ARTIFACTS ?? '../artifacts/media';

export default defineConfig({
  testDir: './e2e',
  testMatch: /media-(reliability|fuzz)\.spec\.ts/,
  timeout: 180_000,
  expect: { timeout: 10_000 },
  workers: 1,
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  outputDir: `${artifactRoot}/test-results`,
  reporter: [
    ['line'],
    ['html', { outputFolder: `${artifactRoot}/html-report`, open: 'never' }],
    ['json', { outputFile: `${artifactRoot}/results.json` }]
  ],
  use: {
    baseURL: process.env.KLISI_MEDIA_BASE_URL ?? 'http://klisi:8080',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure'
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        permissions: ['camera', 'microphone'],
        launchOptions: {
          args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream']
        }
      }
    },
    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'] }
    },
    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] }
    }
  ]
});
