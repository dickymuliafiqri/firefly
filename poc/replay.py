import socket, ssl, sys, glob, gzip, zlib

def send(raw, label):
    ctx = ssl.create_default_context()
    s = ctx.wrap_socket(socket.create_connection(("opencode.ai", 443), timeout=25), server_hostname="opencode.ai")
    s.sendall(raw)
    s.settimeout(25)
    buf = b""
    try:
        while len(buf) < 8000:
            d = s.recv(8192)
            if not d:
                break
            buf += d
            if b"\r\n\r\n" in buf:
                head, _, body = buf.partition(b"\r\n\r\n")
                if b"content-length" in head.lower():
                    cl = int(head.lower().split(b"content-length:")[1].split(b"\r\n")[0])
                    if len(body) >= cl:
                        break
    except Exception:
        pass
    s.close()
    head, _, body = buf.partition(b"\r\n\r\n")
    status = head.split(b"\r\n")[0].decode("latin1", "replace")
    enc = b"x"
    if b"content-encoding" in head.lower():
        enc = head.lower().split(b"content-encoding:")[1].split(b"\r\n")[0].strip()
    if enc == b"gzip":
        try: body = gzip.decompress(body)
        except Exception: pass
    elif enc == b"br":
        pass
    txt = body[:250].decode("utf-8", "replace")
    print(f"{label:34s} {status}  {txt.strip()[:150]}")
    return status

if __name__ == "__main__":
    for f in sorted(glob.glob("/tmp/req_raw_*.bin")):
        raw = open(f, "rb").read()
        first = raw.split(b"\r\n")[0].decode("latin1")
        if "POST" not in first:
            continue
        # strip proxy-absolute-form if any
        send(raw, f"{f.split('_')[-1][:10]} {first[:26]}")
