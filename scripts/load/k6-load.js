// k6 load test — sustained load profile for a running HiTechCloud instance.
//
// Usage:
//   k6 run scripts/load/k6-load.js
//   BASE_URL=https://lic.example.com k6 run scripts/load/k6-load.js
//   BASE_URL=… LICENSE_KEY=KG-… LOGIN_EMAIL=demo@example.com k6 run scripts/load/k6-load.js
//
// Traffic mix (weighted):
//   - GET  /health                        (liveness)
//   - GET  /api/v1/config                 (SPA boot)
//   - GET  /api/v1/marketplace/products   (storefront browse)
//   - POST /api/v1/license/verify         (SDK polling — the hot path)
//   - POST /api/v1/license/activate/deactivate (activation churn, when
//     LICENSE_KEY is set)
//   - POST /api/v1/auth/dev-login         (login bursts, when LOGIN_EMAIL
//     is set — development environments only)
//
// Thresholds: p95 < 500 ms, p99 < 1500 ms, error rate < 1 %.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:9000';
const LICENSE_KEY = __ENV.LICENSE_KEY || '';
const LOGIN_EMAIL = __ENV.LOGIN_EMAIL || '';
const VUS = Number(__ENV.VUS || 20);
const DURATION = __ENV.DURATION || '5m';

const errorRate = new Rate('errors');

export const options = {
  scenarios: {
    load: {
      executor: 'ramping-vus',
      startVUs: 2,
      stages: [
        { duration: '1m', target: VUS },
        { duration: DURATION, target: VUS },
        { duration: '1m', target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<500', 'p(99)<1500'],
    errors: ['rate<0.01'],
  },
};

function tag(res, name) {
  const ok = check(res, {
    [`${name}: 2xx/3xx`]: (r) => r.status >= 200 && r.status < 400,
  });
  errorRate.add(!ok);
}

const jsonHeaders = { 'Content-Type': 'application/json' };

export default function () {
  // Weighted mix; Math.random is seeded per-VU-iteration by k6's scenario
  // engine well enough for a traffic blend.
  const r = Math.random();

  if (r < 0.15) {
    tag(http.get(`${BASE_URL}/health`), 'health');
  } else if (r < 0.35) {
    tag(http.get(`${BASE_URL}/api/v1/config`), 'config');
  } else if (r < 0.60) {
    tag(http.get(`${BASE_URL}/api/v1/marketplace/products?limit=20`), 'marketplace');
  } else if (r < 0.90 && LICENSE_KEY !== '') {
    // SDK polling: verify is the high-volume endpoint (plan §62).
    tag(
      http.post(
        `${BASE_URL}/api/v1/license/verify`,
        JSON.stringify({ license_key: LICENSE_KEY, identifier: 'k6-load-device' }),
        { headers: jsonHeaders },
      ),
      'license verify',
    );
  } else if (r < 0.95 && LICENSE_KEY !== '') {
    // Activation churn: activate then deactivate the same identifier.
    const id = `k6-load-${__VU}-${__ITER}`;
    tag(
      http.post(
        `${BASE_URL}/api/v1/license/activate`,
        JSON.stringify({ license_key: LICENSE_KEY, identifier: id }),
        { headers: jsonHeaders },
      ),
      'license activate',
    );
    tag(
      http.post(
        `${BASE_URL}/api/v1/license/deactivate`,
        JSON.stringify({ license_key: LICENSE_KEY, identifier: id }),
        { headers: jsonHeaders },
      ),
      'license deactivate',
    );
  } else if (LOGIN_EMAIL !== '') {
    const res = http.post(
      `${BASE_URL}/api/v1/auth/dev-login`,
      JSON.stringify({ email: LOGIN_EMAIL }),
      { headers: jsonHeaders },
    );
    // dev-login is development-only; any non-500 answer counts as served.
    const ok = check(res, { 'login: answered': (x) => x.status > 0 && x.status !== 500 });
    errorRate.add(!ok);
  } else {
    tag(http.get(`${BASE_URL}/api/v1/marketplace/categories`), 'categories');
  }

  sleep(0.2);
}
