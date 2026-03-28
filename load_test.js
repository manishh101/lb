import http from 'k6/http';
import { check, sleep } from 'k6';

// This is a professional Load Testing configuration using Grafana k6
export const options = {
  // Stage 1: Ramp up traffic from 1 to 50 Virtual Users (VUs) over 10 seconds
  // Stage 2: Hold the traffic at 50 VUs for 30 seconds to gather sustained metrics
  // Stage 3: Ramp down to 0 VUs over 10 seconds (Graceful stop)
  stages: [
    { duration: '10s', target: 50 },
    { duration: '30s', target: 50 },
    { duration: '10s', target: 0 },
  ],
  thresholds: {
    // We expect 95% of requests to complete under 200ms
    http_req_duration: ['p(95)<200'], 
  },
};

export default function () {
  // 1. Send request to the "Default Router" which forwards to Gamma/Delta
  const res1 = http.get('http://loadbalancer:8082/');
  
  // 2. Send request to the "Payment Router" which forwards to Alpha (Fast-Backends)
  const res2 = http.get('http://loadbalancer:8082/api/payment/process');

  // Verify the requests (Expect 200 OK, or 429 Rate Limited depending on load)
  check(res1, {
    'Default route is 200 or 429': (r) => r.status === 200 || r.status === 429,
  });

  check(res2, {
    'Payment route is 200 or 429': (r) => r.status === 200 || r.status === 429,
  });

  // Wait 100ms to 500ms between requests to simulate real-world browser delays
  sleep(Math.random() * 0.4 + 0.1);
}
