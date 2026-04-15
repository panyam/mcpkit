import { useState, useCallback } from "react";
import { useMCPApp, useMCPEvent } from "./useMCPApp";

function App() {
  const { connected, hostContext, callTool } = useMCPApp();
  const [serverTime, setServerTime] = useState("Loading...");
  const [messageText, setMessageText] = useState("This is message text.");
  const [messageStatus, setMessageStatus] = useState("");
  const [logText, setLogText] = useState("This is log text.");
  const [logStatus, setLogStatus] = useState("");
  const [linkUrl, setLinkUrl] = useState("https://modelcontextprotocol.io/");
  const [linkStatus, setLinkStatus] = useState("");

  // Listen for tool results pushed by the host (LLM-initiated calls).
  useMCPEvent("toolresult", useCallback((data: MCPAppEventMap["toolresult"]) => {
    const result = data.result as any;
    const time = result?.structuredContent?.time || result?.content?.[0]?.text;
    if (time) setServerTime(time);
  }, []));

  const handleGetTime = async () => {
    try {
      const res = await callTool("get-time", {});
      const text = res.content?.[0]?.text;
      if (text) setServerTime(text);
    } catch (e) {
      setServerTime("[ERROR]");
      console.error(e);
    }
  };

  const handleSendMessage = async () => {
    setMessageStatus("Sending...");
    try {
      await MCPApp.sendMessage({
        role: "user",
        content: [{ type: "text", text: messageText }],
      }, { timeout: 5000 });
      setMessageStatus("Sent!");
    } catch (e) {
      setMessageStatus("Error: " + (e instanceof Error ? e.message : String(e)));
      console.error("Message send error:", e);
    }
  };

  const handleSendLog = () => {
    MCPApp.log("info", logText);
    setLogStatus("Log sent (check host log panel)");
    setTimeout(() => setLogStatus(""), 2000);
  };

  const handleOpenLink = () => {
    MCPApp.openLink(linkUrl);
    setLinkStatus("Link sent to host");
    setTimeout(() => setLinkStatus(""), 2000);
  };

  const theme = hostContext?.theme || "unknown";

  return (
    <main style={{ padding: "1.5em", fontFamily: "system-ui" }}>
      <p style={{ fontSize: "0.85em", color: "#888" }}>
        {connected
          ? `Connected (${theme} mode) — watch activity in DevTools console`
          : "Connecting..."}
      </p>

      <Section title="Server Time">
        <p><strong>Time:</strong> <code>{serverTime}</code></p>
        <button onClick={handleGetTime} disabled={!connected}>
          Get Server Time
        </button>
      </Section>

      <Section title="Send Message">
        <textarea
          value={messageText}
          onChange={(e) => setMessageText(e.target.value)}
          rows={3}
          style={{ width: "100%", resize: "vertical" }}
        />
        <button onClick={handleSendMessage} disabled={!connected}>
          Send Message
        </button>
        {messageStatus && <Status text={messageStatus} />}
      </Section>

      <Section title="Send Log">
        <input
          type="text"
          value={logText}
          onChange={(e) => setLogText(e.target.value)}
          style={{ width: "100%" }}
        />
        <button onClick={handleSendLog} disabled={!connected}>
          Send Log
        </button>
        {logStatus && <Status text={logStatus} />}
      </Section>

      <Section title="Open Link">
        <input
          type="url"
          value={linkUrl}
          onChange={(e) => setLinkUrl(e.target.value)}
          style={{ width: "100%" }}
        />
        <button onClick={handleOpenLink} disabled={!connected}>
          Open Link
        </button>
        {linkStatus && <Status text={linkStatus} />}
      </Section>
    </main>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{
      marginBottom: "1.5em",
      padding: "1em",
      border: "1px solid var(--color-border-primary, #ddd)",
      borderRadius: "8px",
    }}>
      <h3 style={{ margin: "0 0 0.5em" }}>{title}</h3>
      {children}
    </div>
  );
}

function Status({ text }: { text: string }) {
  return <p style={{ fontSize: "0.85em", color: "#888", margin: "0.5em 0 0" }}>{text}</p>;
}

export default App;
