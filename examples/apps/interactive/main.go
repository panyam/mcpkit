// Example: Tic-Tac-Toe MCP App with app-provided tools.
//
// The Go server manages game state (new_game tool). The HTML app registers
// tools via registerTool() that let the model play: make_move and get_board.
// The user plays by clicking cells in the iframe.
//
// Run:  go run . -addr :8080
// Connect a host to http://localhost:8080/mcp, ask "let's play tic-tac-toe".
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

//go:embed tictactoe.html
var tictactoeTemplateRaw string

type pageData struct {
	Bridge ui.BridgeData
}

// Game state — shared between tool handlers.
var (
	board    [9]string // "", "X", "O"
	turn     string    // "X" or "O"
	gameMu   sync.Mutex
	gameOver bool
)

func resetGame() {
	board = [9]string{}
	turn = "X"
	gameOver = false
}

func checkWinner() string {
	lines := [][3]int{
		{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, // rows
		{0, 3, 6}, {1, 4, 7}, {2, 5, 8}, // cols
		{0, 4, 8}, {2, 4, 6},             // diagonals
	}
	for _, l := range lines {
		if board[l[0]] != "" && board[l[0]] == board[l[1]] && board[l[1]] == board[l[2]] {
			return board[l[0]]
		}
	}
	// Check draw.
	full := true
	for _, c := range board {
		if c == "" {
			full = false
			break
		}
	}
	if full {
		return "draw"
	}
	return ""
}

func init() {
	resetGame()
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	tmpl := template.Must(template.New("tictactoe").Parse(tictactoeTemplateRaw))
	template.Must(tmpl.Parse(ui.BridgeTemplateDef()))

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, pageData{
		Bridge: ui.NewBridgeData("tictactoe-app", "0.1.0"),
	}); err != nil {
		log.Fatal(err)
	}
	gameHTML := buf.String()

	srv := server.NewServer(
		core.ServerInfo{Name: "tictactoe-app", Version: "0.1.0"},
		server.WithExtension(&ui.UIExtension{}),
	)

	// Server-side tool: new_game — resets the board.
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, string]{
		Name:        "new_game",
		Description: "Start a new tic-tac-toe game. Resets the board.",
		Handler: func(ctx core.ToolContext, _ struct{}) (string, error) {
			gameMu.Lock()
			resetGame()
			gameMu.Unlock()
			return "New game started. X goes first. The user plays by clicking cells in the app. You (the model) can play by calling the make_move app tool with a position 0-8.", nil
		},
		ResourceURI: "ui://tictactoe/board",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: gameHTML,
			}}}, nil
		},
	})

	// Server-side tool: get_game_state — returns full game state for the model.
	type gameStateResult struct {
		Board    [9]string `json:"board"`
		Turn     string    `json:"turn"`
		GameOver bool      `json:"gameOver"`
		Winner   string    `json:"winner"`
	}
	srv.Register(core.TypedTool[struct{}, gameStateResult]("get_game_state",
		"Get the current game state (board, whose turn, winner)",
		func(ctx core.ToolContext, _ struct{}) (gameStateResult, error) {
			gameMu.Lock()
			defer gameMu.Unlock()
			return gameStateResult{
				Board: board, Turn: turn, GameOver: gameOver, Winner: checkWinner(),
			}, nil
		},
	))

	// Server-side tool: server_move — model places a piece via server tool.
	// This is the server-side alternative to the app-provided make_move tool.
	type moveInput struct {
		Position int `json:"position" jsonschema:"description=Board position 0-8 (top-left=0 to bottom-right=8)"`
	}
	srv.Register(core.TextTool[moveInput]("server_move",
		"Place your piece (model plays as O). Position 0-8.",
		func(ctx core.ToolContext, input moveInput) (string, error) {
			gameMu.Lock()
			defer gameMu.Unlock()

			if gameOver {
				return "Game is over. Call new_game to start a new one.", nil
			}
			if input.Position < 0 || input.Position > 8 {
				return fmt.Sprintf("Invalid position %d. Must be 0-8.", input.Position), nil
			}
			if board[input.Position] != "" {
				return fmt.Sprintf("Position %d is already taken by %s.", input.Position, board[input.Position]), nil
			}

			board[input.Position] = turn
			winner := checkWinner()
			prevTurn := turn
			if turn == "X" {
				turn = "O"
			} else {
				turn = "X"
			}
			if winner != "" {
				gameOver = true
				if winner == "draw" {
					return fmt.Sprintf("%s placed at position %d. Game is a draw!", prevTurn, input.Position), nil
				}
				return fmt.Sprintf("%s placed at position %d. %s wins!", prevTurn, input.Position, winner), nil
			}
			return fmt.Sprintf("%s placed at position %d. %s's turn.", prevTurn, input.Position, turn), nil
		},
	))

	log.Printf("tictactoe-app listening on %s (MCP at /mcp)", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
