// k6 smoke test — quick health check of a running HiTechCloud instance.
//
// Usage:
//   k6 run scripts/load/k6-smoke.js
//   BASE_URL=https://lic.example.com k6 run scripts/load/k6-smoke.js
//   BASE_URL=http://localhost:9000 LICENSE_KEY=KG-… k6 run scripts/load/k6-smoke.js
//
// Scenarios: /health, /api/v1/config, /api/v1/marketplace/products,
// license verify (when LICENSE_KEY is set), login (dev-login probe when
// LOGIN_EMAIL is set). Thresholds: p95 < 500 ms, error rate < 1 %.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:9000';
const LICENSE_KEY = __ENV.LICENSE_KEY || '';
const LICENSE_IDENTIFIER = __ENV.LICENSE_IDENTIFIER || 'k6-smoke-device';
const LOGIN_EMAIL = __ENV.LOGIN_EMAIL || '';

const errorRate = new Rate('errors');

export const options = {
  scenarios: {
    smoke: {
      executor: 'per-vu-iterations',
      vus: 2,
      iterations: 20,
      maxDuration: '2m',
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<500'],
    errors: ['rate<0.01'],
  },
};

function tag(res, name) {
  const ok = check(res, {
    [`${name}: status 2xx`]: (r) => r.status >= 200 && r.status < 400,
    [`${name}: fast`]: (r) => r.timings.duration < 500,
  });
  errorRate.add(!ok);
  return ok;
}

export default function () {
  // 1. Health (root path — not under /api/v1).
  tag(http.get(`${BASE_URL}/health`), 'health');

  // 2. Public config (surfaces + attribution fields).
  tag(http.get(`${BASE_URL}/api/v1/config`), 'config');

  // 3. Marketplace catalogue.
  tag(http.get(`${BASE_URL}/api/v1/marketplace/products?limit=20`), 'marketplace');

  // 4. License verify flow (needs a real key; skipped otherwise).
  if (LICENSE_KEY !== '') {
    const res = http.post(
      `${BASE_URL}/api/v1/license/verify`,
      JSON.stringify({ license_key: LICENSE_KEY, identifier: LICENSE_IDENTIFIER }),
      { headers: { 'Content-Type': 'application/json' } },
    );
    const ok = check(res, {
      'license verify: 2xx': (r) => r.status >= 200 && r.status < 300,
      'license verify: valid envelope': (r) => {
        try {
          return JSON.parse(r.body).success === true;
        } catch {
          return false;
        }
      },
    });
    errorRate.add(!ok);
  }

  // 5. Login probe (dev-login only answers in ENVIRONMENT=development;
  //    in other environments we accept 401/403/404 as a correct refusal).
  if (LOGIN_EMAIL !== '') {
    const res = http.post(
      `${BASE_URL}/api/v1/auth/dev-login`,
      JSON.stringify({ email: LOGIN_EMAIL }),
      { headers: { 'Content-Type': 'application/json' } },
    );
    const ok = check(res, {
      'login: answered': (r) => r.status > 0 && r.status !== 500,
    });
    errorRate.add(!ok);
  }

  sleep(0.3);
}
