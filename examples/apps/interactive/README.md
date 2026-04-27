# Tic-Tac-Toe — Interactive MCP App

A tic-tac-toe game where the user plays by clicking cells in the iframe and the model plays by calling app-provided tools. Demonstrates the full bidirectional app-provided tools pattern from the ext-apps spec.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `core.TextTool`, `core.TypedTool`, `server.Run` |
| Extension | `ext/ui` — `UIExtension`, `RegisterTypedAppTool`, `BridgeTemplateDef`, `NewBridgeData` |
| Bridge | `MCPApp.registerTool()`, `MCPApp.callTool()`, `MCPApp.sendToolListChanged()` |
| MCP primitives | Tools, Resources (App), App-provided tools (bidirectional) |

## What it demonstrates

- **App-provided tools** via `registerTool()`: `make_move` and `get_board`
- **Server tools**: `new_game`, `server_move`, `get_game_state`
- **Bidirectional flow**: user clicks → app calls server tool; model calls app tool → app updates board
- **Tool lifecycle**: `sendToolListChanged()` on game reset

## Sequence Diagrams

### User makes a move (app→host→server)

```mermaid
sequenceDiagram
    participant User
    participant App as Tic-Tac-Toe (iframe)
    participant Host
    participant Server as Go Server

    User->>App: clicks cell 4
    App->>Host: MCPApp.callTool("server_move", {position: 4})
    Host->>Server: tools/call server_move
    Server-->>Host: "X placed at 4. O's turn."
    Host-->>App: response
    App->>Host: MCPApp.callTool("get_game_state")
    Host->>Server: tools/call get_game_state
    Server-->>Host: {board, turn, gameOver}
    Host-->>App: response
    App->>App: renderBoard() — X appears at center
```

### Model makes a move (host→app tool)

```mermaid
sequenceDiagram
    participant LLM
    participant Host
    participant App as Tic-Tac-Toe (iframe)

    LLM->>Host: "I'll take the corner"
    Host->>App: tools/call {name: "make_move", args: {position: 0}}
    App->>App: board[0] = "O"; renderBoard()
    App-->>Host: {text: "O placed at 0. X's turn."}
    Host-->>LLM: "O placed at position 0. X's turn."
```

### Model reads board state

```mermaid
sequenceDiagram
    participant LLM
    participant Host
    participant App as Tic-Tac-Toe (iframe)

    LLM->>Host: "Let me check the board"
    Host->>App: tools/call {name: "get_board"}
    App-->>Host: {board: ["X","","O",...], turn: "X", visual: "X | 1 | O\n..."}
    Host-->>LLM: Board state with visual representation
```

## Setup

```bash
cd examples/apps/interactive
go run . -addr :8080
```

## Connect a host

In MCPJam (or Claude Desktop):
1. Add server: `http://localhost:8080/mcp` (Streamable HTTP)
2. Server name: "Tic-Tac-Toe"

## Prompts to try

- "Let's play tic-tac-toe" — model calls `new_game`, sees the board
- "I'll go first, take the center" — user clicks center cell
- "Your turn" — model calls `get_board` to see state, then `make_move` to play
- "Start a new game" — resets board, `sendToolListChanged()` fires

## Tools

| Tool | Source | Description |
|------|--------|-------------|
| `new_game` | Server | Reset the board, X goes first |
| `server_move` | Server | Place a piece (alternative to app tool) |
| `get_game_state` | Server | Get full game state as structured JSON |
| `make_move` | App (registerTool) | Place a piece — model plays as O |
| `get_board` | App (registerTool) | Get board state with visual grid |

## Key files

| File | What |
|------|------|
| `tictactoe.html` | HTML with bridge + game logic + `registerTool()` |
| `main.go` | Go server: game state, new_game, server_move, get_game_state |
