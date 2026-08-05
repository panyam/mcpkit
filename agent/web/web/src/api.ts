import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { HostService } from "./gen/mcpkit/agentweb/v1/host_pb.js";

// hostClient returns a Connect client for HostService. baseUrl defaults to the
// page's own origin ("/"), where serve.go mounts the Connect handlers, so there
// is no CORS or proxy to configure. Connect server-streaming (Watch) rides the
// Connect protocol over HTTP/1.1, so no h2c is needed for local dev.
export function hostClient(baseUrl = "/"): Client<typeof HostService> {
  return createClient(HostService, createConnectTransport({ baseUrl }));
}
