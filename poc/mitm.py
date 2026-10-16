import socket, ssl, threading, struct, sys, json, time

LOG = "/tmp/mitm.log"
lock = threading.Lock()

def w(m):
    with lock:
        with open(LOG, "a") as f:
            f.write(m + "\n")
    print(m[:1500], flush=True)

ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("/tmp/mitm_srv.pem", "/tmp/mitm_srv.key")
ctx.set_alpn_protocols(["http/1.1"])

def pipe(a, b):
    try:
        while True:
            d = a.recv(65536)
            if not d:
                break
            b.sendall(d)
    except Exception:
        pass
    finally:
        try: b.shutdown(socket.SHUT_WR)
        except Exception: pass

def handle(c):
    up = None
    try:
        c.settimeout(60)
        buf = b""
        while b"\r\n\r\n" not in buf:
            d = c.recv(4096)
            if not d: return
            buf += d
        head, _, rest = buf.partition(b"\r\n\r\n")
        line = head.split(b"\r\n")[0].decode("latin1")
        if not line.startswith("CONNECT"):
            w("### NON-CONNECT " + line); return
        target = line.split()[1]
        host = target.split(":")[0]
        c.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
        tc = ctx.wrap_socket(c, server_side=True)
        w(f"### TLS OK {host} alpn={tc.selected_alpn_protocol()}")
        req = rest
        tc.settimeout(30)
        while b"\r\n\r\n" not in req:
            d = tc.recv(65536)
            if not d: return
            req += d
        rhead, _, rbody = req.partition(b"\r\n\r\n")
        clen = 0
        for hl in rhead.split(b"\r\n")[1:]:
            if hl.lower().startswith(b"content-length:"):
                clen = int(hl.split(b":")[1].strip())
        while len(rbody) < clen:
            d = tc.recv(65536)
            if not d: break
            rbody += d
        w("REQUEST-LINE " + rhead.split(b"\r\n")[0].decode("latin1"))
        w("HEADERS\n" + rhead.decode("latin1", "replace"))
        w("BODY(" + str(len(rbody)) + ")\n" + rbody.decode("utf-8", "replace")[:3000])
        raw = rhead + b"\r\n\r\n" + rbody
        with open(f"/tmp/req_raw_{int(time.time()*1000)}.bin", "wb") as f:
            f.write(raw)
        w("RAWHEAD " + repr(rhead[:1800]))
        up = ssl.create_default_context().wrap_socket(socket.create_connection((host, 443), timeout=30), server_hostname=host)
        up.sendall(raw)
        up.settimeout(30)
        first = up.recv(65536)
        w("RESP-FIRST " + first.split(b"\r\n")[0].decode("latin1", "replace"))
        if b"\r\n\r\n" in first:
            w("RESP-HEAD\n" + first.split(b"\r\n\r\n")[0].decode("latin1", "replace"))
            w("RESP-BODY " + first.split(b"\r\n\r\n", 1)[1][:600].decode("utf-8", "replace"))
        tc.sendall(first)
        threading.Thread(target=pipe, args=(tc, up), daemon=True).start()
        pipe(up, tc)
    except Exception as e:
        w("ERR " + repr(e))
    finally:
        for s in (c, up):
            try:
                if s: s.close()
            except Exception: pass

if __name__ == "__main__":
    open(LOG, "w").close()
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 9998)); s.listen(64)
    print("mitm on 127.0.0.1:9998", flush=True)
    while True:
        conn, _ = s.accept()
        threading.Thread(target=handle, args=(conn,), daemon=True).start()
