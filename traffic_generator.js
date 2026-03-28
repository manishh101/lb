import http from 'k6/http';
import { check, sleep, group } from 'k6';

/**
 * COMPREHENSIVE TRAFFIC GENERATOR & FEATURE TESTER (k6 VERSION)
 * -----------------------------------------------------------
 * Target: Intelligent Load Balancer
 * Network: docker-compose lb-network
 */

export const options = {
  // Constant load to simulate continuous traffic
  vus: 10,
  duration: '5m',
  thresholds: {
    'http_req_failed': ['rate<0.1'], // Error rate should be less than 10%
  },
};

const LB_URL = 'http://loadbalancer:8082';
const DASHBOARD_URL = 'http://loadbalancer:8081';

// Internal backend URLs for Chaos Monkey
const BACKENDS = [
  'http://alpha:8001/toggle',
  'http://beta:8002/toggle',
  'http://gamma:8003/toggle',
  'http://delta:8004/toggle'
];

export default function () {
  
  // 1. Normal Traffic (Success)
  group('Normal Traffic', () => {
    let res = http.get(`${LB_URL}/`);
    check(res, { 'status is OK or RateLimited': (r) => [200, 429].includes(r.status) });
  });

  // 2. Feature Routing (Payment/Checkout)
  group('Feature Routing', () => {
    let endpoints = ['/api/payment', '/api/checkout'];
    let endpoint = endpoints[Math.floor(Math.random() * endpoints.length)];
    let res = http.get(`${LB_URL}${endpoint}`);
    check(res, { 'feature route status OK': (r) => [200, 429].includes(r.status) });
  });

  // 3. Admin Security (Header requirement)
  group('Admin Security', () => {
    // Authorized
    let authRes = http.get(`${LB_URL}/admin`, { headers: { 'X-Admin-Key': 'secret' } });
    check(authRes, { 'admin authorized is 200': (r) => r.status === 200 });

    // Unauthorized (Blocked by router rule -> should 404)
    let unauthRes = http.get(`${LB_URL}/admin`);
    check(unauthRes, { 'admin unauthorized is 404': (r) => r.status === 404 });
  });

  // 4. Dashboard Authentication (Basic Auth)
  group('Dashboard Auth', () => {
    // Valid login
    let dashRes = http.get(`${DASHBOARD_URL}/`, { auth: 'admin:loadbalancer' });
    check(dashRes, { 'dashboard login OK': (r) => r.status === 200 });

    // Invalid login
    let dashFailRes = http.get(`${DASHBOARD_URL}/`, { auth: 'admin:wrong' });
    check(dashFailRes, { 'dashboard login Fail': (r) => r.status === 401 });
  });

  // 5. Chaos Monkey (Occasional backend toggling)
  // We only run this occasionally to simulate random failures
  if (__ITER % 20 === 0) {
    group('Chaos Monkey', () => {
      let target = BACKENDS[Math.floor(Math.random() * BACKENDS.length)];
      console.log(`[CHAOS] Toggling backend: ${target}`);
      http.get(target);
    });
  }

  // 6. Edge Case: 404 Garbage Traffic
  group('404 Resilience', () => {
    let res = http.get(`${LB_URL}/unknown-${Math.random()}`);
    check(res, { '404 handled gracefully': (r) => r.status === 404 });
  });

  // Simulate user delay
  sleep(Math.random() * 0.5 + 0.1);
}
