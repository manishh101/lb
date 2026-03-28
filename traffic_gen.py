import random
import time
import urllib.request
import urllib.error
import threading
import base64

def send_request(url, headers=None):
    req = urllib.request.Request(url, headers=headers or {})
    try:
        urllib.request.urlopen(req, timeout=5)
    except Exception:
        pass

def normal_traffic():
    while True:
        # Default router -> all-backends
        send_request("http://localhost:8082/")
        time.sleep(random.uniform(0.05, 0.2))

def fast_traffic():
    while True:
        # Payment router -> fast-backends
        send_request("http://localhost:8082/api/checkout")
        time.sleep(random.uniform(0.1, 0.4))

def admin_traffic():
    auth = base64.b64encode(b"admin:secret").decode("ascii")
    headers = {
        "X-Admin-Key": "secret",
        "Authorization": f"Basic {auth}"
    }
    while True:
        # Admin router -> admin-backends
        send_request("http://localhost:8082/admin", headers)
        time.sleep(random.uniform(0.3, 1.0))

def failing_auth_traffic():
    headers = {"X-Admin-Key": "secret", "Authorization": "Basic wrong"}
    while True:
        send_request("http://localhost:8082/admin", headers)
        time.sleep(random.uniform(2.0, 5.0))
        
def burst_traffic():
    while True:
        # Huge burst every 10 seconds to trigger rate limits (100 req/sec limit)
        for _ in range(150):
            threading.Thread(target=send_request, args=("http://localhost:8082/",)).start()
        time.sleep(10)

def chaos_monkey():
    backends = [
        "http://localhost:8001/toggle",
        "http://localhost:8002/toggle",
        "http://localhost:8003/toggle",
        "http://localhost:8004/toggle"
    ]
    while True:
        time.sleep(random.uniform(15, 30))
        # Toggle a random backend to simulate failure and recovery
        target = random.choice(backends)
        send_request(target)

if __name__ == "__main__":
    print("Starting simulated real-world traffic...")
    threads = [
        threading.Thread(target=normal_traffic, daemon=True),
        threading.Thread(target=fast_traffic, daemon=True),
        threading.Thread(target=admin_traffic, daemon=True),
        threading.Thread(target=failing_auth_traffic, daemon=True),
        threading.Thread(target=burst_traffic, daemon=True),
        threading.Thread(target=chaos_monkey, daemon=True)
    ]
    for t in threads:
        t.start()
        
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print("Stopping traffic generator.")
