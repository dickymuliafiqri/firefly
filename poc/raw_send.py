import socket, ssl, sys, json

HOST = "opencode.ai"
PATH = "/inference/v1/chat/completions"
UA = "opencode/latest/2.0.29/cli"
SID = "ses_ec0666878ffejlB4l3dpzn3ykl"
PID = "ac4a3f5b37ef5003cf1a0366dc18410ab73129bb"
TR = "094f014910148c1a3f19977d1e0d9f5a-a02738f756721217"

BODY = json.dumps({"model": "big-pickle", "messages": [{"role": "user", "content": "hi"}], "stream": False})

def full_headers():
    return [
        ("Authorization", "Bearer public"),
        ("Content-Type", "application/json"),
        ("User-Agent", UA),
        ("b3", f"{TR}-1-30d26b0ceeaba461"),
        ("traceparent", f"00-{TR}-01"),
        ("x-opencode-client", "cli"),
        ("x-opencode-project", PID),
        ("x-opencode-session", SID),
        ("x-opencode-session-id", SID),
        ("x-session-affinity", SID),
        ("x-session-id", SID),
        ("Connection", "keep-alive"),
        ("Accept", "*/*"),
        ("Accept-Encoding", "gzip, deflate, br, zstd"),
    ]

VARIANTS = {
    "exact-order": full_headers(),
    "reversed": list(reversed(full_headers())),
    "minimal": [("Authorization", "Bearer public"), ("Content-Type", "application/json")],
    "no-auth": [("Content-Type", "application/json"), ("User-Agent", UA)],
    "ua-hex-session": [("Authorization", "Bearer public"), ("Content-Type", "application/json"),
                       ("User-Agent", UA), ("x-opencode-client", "cli"),
                       ("x-opencode-session", "ses_0123456789abcdefghijklmnop")],
    "te-cl": [("Authorization", "Bearer public"), ("Content-Type", "application/json"),
              ("Transfer-Encoding", "identity")],
}

def send(name, hdrs, body=BODY, path=PATH, http11=True):
    ctx = ssl.create_default_context()
    s = ctx.wrap_socket(socket.create_connection((HOST, 443), timeout=25), server_hostname=HOST)
    lines = [f"POST {path} HTTP/1.1", f"Host: {HOST}"]
    for k, v in hdrs:
        lines.append(f"{k}: {v}")
    lines.append(f"Content-Length: {len(body.encode())}")
    raw = ("\r\n".join(lines) + "\r\n\r\n").encode() + body.encode()
    s.sendall(raw)
    buf = b""
    s.settimeout(25)
    try:
        while len(buf) < 4000:
            d = s.recv(4096)
            if not d:
                break
            buf += d
    except Exception:
        pass
    s.close()
    status = buf.split(b"\r\n")[0].decode("latin1", "replace")
    body_out = buf.split(b"\r\n\r\n", 1)[1][:220].decode("utf-8", "replace") if b"\r\n\r\n" in buf else ""
    print(f"{name:16s} {status}")
    if "200" not in status:
        print(f"                 {body_out.strip()[:180]}")
    else:
        print(f"                 {body_out.strip()[:180]}")
    return buf

if __name__ == "__main__":
    which = sys.argv[1:] or list(VARIANTS)
    for n in which:
        try:
            send(n, VARIANTS[n])
        except Exception as e:
            print(f"{n:16s} EXC {e!r}")
