"""Generate a deterministic, synthetic web access log for the demo."""
import random

random.seed(7)
paths = ["/", "/login", "/api/users", "/api/orders", "/static/app.js", "/static/logo.png",
         "/blog/pipes", "/old-page", "/wp-admin", "/api/search", "/favicon.ico", "/healthz"]
weights = [30, 8, 20, 15, 25, 20, 6, 4, 3, 10, 12, 18]
status_for = {"/old-page": [404], "/wp-admin": [404, 403], "/favicon.ico": [200, 404],
              "/api/search": [200, 200, 500], "/api/orders": [200, 200, 200, 500, 502]}
agents = ["Mozilla/5.0", "curl/8.4.0", "Googlebot/2.1", "kube-probe/1.29"]
for i in range(1500):
    p = random.choices(paths, weights)[0]
    status = random.choice(status_for.get(p, [200, 200, 200, 304]))
    ip = f"10.0.{random.randint(0, 9)}.{random.randint(2, 250)}"
    agent = "kube-probe/1.29" if p == "/healthz" else random.choice(agents[:3])
    size = random.randint(200, 50000)
    sec = i * 2
    print(f'{ip} - - [24/Nov/2026:09:{sec // 60 % 60:02d}:{sec % 60:02d} +0000] '
          f'"GET {p} HTTP/1.1" {status} {size} "-" "{agent}"')
