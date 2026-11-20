// MITM proxy: terminates TLS for opencode.ai, logs the exact wire request
// (header ORDER included), then forwards to the real host.
const http = require("http");
const https = require("https");
const net = require("net");
const tls = require("tls");
const fs = require("fs");
const { URL } = require("url");

const DIR = __dirname;
const cert = fs.readFileSync(`${DIR}/srv.crt`);
const key = fs.readFileSync(`${DIR}/srv.key`);
const PORT = 8898;
const UPSTREAM = "opencode.ai";

let CAPTURE_N = 0;
const log = (...a) => console.log(new Date().toISOString().slice(11, 23), ...a);

// Inner server that receives the decrypted HTTP requests.
const inner = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = Buffer.concat(chunks);
    log("=== CAPTURED REQUEST ===");
    log(`${req.method} ${req.url} HTTP/${req.httpVersion}`);
    // rawHeaders preserves wire order
    for (let i = 0; i < req.rawHeaders.length; i += 2) {
      log(`H  ${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`);
    }
    if (body.length) {
      const n = ++CAPTURE_N;
      fs.writeFileSync(`${DIR}/capture-${n}.json`, body);
      fs.writeFileSync(
        `${DIR}/capture-${n}.headers.json`,
        JSON.stringify({ method: req.method, url: req.url, httpVersion: req.httpVersion, rawHeaders: req.rawHeaders }, null, 2),
      );
      log(`BODY(${body.length}) -> capture-${n}.json`);
    }
    log("=== END CAPTURE ===");

    const fwd = https.request(
      {
        hostname: UPSTREAM,
        port: 443,
        path: req.url,
        method: req.method,
        headers: { ...req.headers, host: UPSTREAM },
      },
      (ur) => {
        const rc = [];
        ur.on("data", (c) => rc.push(c));
        ur.on("end", () => {
          const rb = Buffer.concat(rc);
          log(`UPSTREAM ${ur.statusCode} :: ${rb.toString("utf8").slice(0, 500)}`);
          res.writeHead(ur.statusCode, ur.headers);
          res.end(rb);
        });
      },
    );
    fwd.on("error", (e) => {
      log("UPSTREAM ERROR", String(e).slice(0, 200));
      res.writeHead(502);
      res.end("proxy error");
    });
    if (body.length) fwd.write(body);
    fwd.end();
  });
});

const server = http.createServer();
server.on("connect", (req, clientSocket, head) => {
  const [host, port] = req.url.split(":");
  log(`CONNECT ${req.url}`);
  clientSocket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
  const tlsSocket = new tls.TLSSocket(clientSocket, {
    isServer: true,
    cert,
    key,
    SNICallback: (_servername, cb) => cb(null, tls.createSecureContext({ cert, key })),
  });
  tlsSocket.on("error", (e) => log("TLS ERR", String(e).slice(0, 160)));
  inner.emit("connection", tlsSocket);
});

server.listen(PORT, "127.0.0.1", () => log(`MITM proxy listening on 127.0.0.1:${PORT}`));
