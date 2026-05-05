# Walkthrough graph (auto-generated)

Run `make graph` to regenerate. Source of truth: each page's `**Prerequisites:**` header.
Nodes are clickable — they link to the page on GitHub (planned pages 404 by design).

```mermaid
graph TD
    bringup["bringup<br/>(root)"]
    click bringup "https://github.com/panyam/mcpkit/blob/main/tutorials/walkthrough/bringup.md"
    extension-mechanisms["extension-mechanisms<br/>(root)"]
    click extension-mechanisms "https://github.com/panyam/mcpkit/blob/main/tutorials/walkthrough/extension-mechanisms.md"
    notifications["notifications<br/>(root)"]
    click notifications "https://github.com/panyam/mcpkit/blob/main/tutorials/walkthrough/notifications.md"
    request-anatomy["request-anatomy<br/>(root)"]
    click request-anatomy "https://github.com/panyam/mcpkit/blob/main/tutorials/walkthrough/request-anatomy.md"
    transport-mechanics["transport-mechanics<br/>(root)"]
    click transport-mechanics "https://github.com/panyam/mcpkit/blob/main/tutorials/walkthrough/transport-mechanics.md"

    bringup --> extension-mechanisms
    transport-mechanics --> extension-mechanisms
    notifications --> extension-mechanisms
    request-anatomy --> extension-mechanisms
    bringup --> request-anatomy
    transport-mechanics --> request-anatomy
    notifications --> request-anatomy

    classDef written fill:#e8f5e9,stroke:#2e7d32,color:#000;
    class bringup,extension-mechanisms,notifications,request-anatomy,transport-mechanics written;
```
