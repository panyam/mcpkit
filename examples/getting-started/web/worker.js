// Runs the getting-started MCP server, compiled to wasm, inside a Web Worker.
// In a wasm build, srv.Run installs self.onmessage and answers through
// postMessage, one JSON-RPC message per call (server.RunWorker in mcpkit).
//
// Messages the page sends before Go installs its handler are buffered here
// and replayed. Once the handler is live, the worker posts the object
// {mcpkit: "ready"}. JSON-RPC traffic is always a string, so the page can
// tell the two apart.
importScripts("wasm_exec.js");

const pending = [];
const buffer = (e) => pending.push(e.data);
self.onmessage = buffer;

const go = new Go();
WebAssembly.instantiateStreaming(fetch("server.wasm"), go.importObject)
  .then((result) => {
    go.run(result.instance).then(() => {
      self.postMessage({ mcpkit: "exited" });
    });
    const waitForHandler = () => {
      if (self.onmessage === buffer) {
        setTimeout(waitForHandler, 5);
        return;
      }
      for (const data of pending.splice(0)) self.onmessage({ data });
      self.postMessage({ mcpkit: "ready" });
    };
    waitForHandler();
  })
  .catch((err) => self.postMessage({ mcpkit: "error", message: String(err) }));
