// Example MCP server generated from proto annotations using protoc-gen-go-mcp.
// Demonstrates all three MCP primitives: tools, resources, and prompts.
package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"

	booksv1 "github.com/panyam/mcpkit/examples/protogen/bookservice/gen/bookservice/v1"
)

// catalog is a simple in-memory book database.
var catalog = []*booksv1.Book{
	{Id: "1", Title: "The Go Programming Language", Author: "Donovan & Kernighan", Genre: "programming", Year: 2015, Rating: 4.5},
	{Id: "2", Title: "Designing Data-Intensive Applications", Author: "Martin Kleppmann", Genre: "systems", Year: 2017, Rating: 4.8},
	{Id: "3", Title: "The Pragmatic Programmer", Author: "Hunt & Thomas", Genre: "programming", Year: 1999, Rating: 4.6},
}

// bookService implements the generated BookServiceMCPServer, BookServiceMCPResourceServer,
// and BookServiceMCPPromptServer interfaces.
type bookService struct{}

func (s *bookService) SearchBooks(_ core.ToolContext, req *booksv1.SearchBooksRequest) (*booksv1.SearchBooksResponse, error) {
	var results []*booksv1.Book
	for _, b := range catalog {
		if req.Genre != "" && b.Genre != req.Genre {
			continue
		}
		results = append(results, b)
		if req.MaxResults > 0 && int32(len(results)) >= req.MaxResults {
			break
		}
	}
	return &booksv1.SearchBooksResponse{Results: results, Total: int32(len(results))}, nil
}

func (s *bookService) GetBook(_ core.ResourceContext, req *booksv1.GetBookRequest) (*booksv1.GetBookResponse, error) {
	for _, b := range catalog {
		if b.Id == req.BookId {
			return &booksv1.GetBookResponse{Book: b}, nil
		}
	}
	return nil, fmt.Errorf("book %q not found", req.BookId)
}

func (s *bookService) GetAuthorBooks(_ core.ResourceContext, req *booksv1.GetAuthorBooksRequest) (*booksv1.GetAuthorBooksResponse, error) {
	var books []*booksv1.Book
	for _, b := range catalog {
		if b.Author == req.AuthorId {
			books = append(books, b)
		}
	}
	return &booksv1.GetAuthorBooksResponse{Books: books}, nil
}

func (s *bookService) SummarizeBook(_ core.PromptContext, req *booksv1.SummarizeBookRequest) (*booksv1.SummarizeBookResponse, error) {
	for _, b := range catalog {
		if b.Id == req.BookId {
			return &booksv1.SummarizeBookResponse{
				Summary: fmt.Sprintf("%s by %s (%d) - %s", b.Title, b.Author, b.Year, b.Synopsis),
			}, nil
		}
	}
	return nil, fmt.Errorf("book %q not found", req.BookId)
}

func (s *bookService) RecommendBooks(_ core.PromptContext, req *booksv1.RecommendBooksRequest) (*booksv1.RecommendBooksResponse, error) {
	var recs []*booksv1.Book
	for _, b := range catalog {
		if req.Genre != "" && b.Genre != req.Genre {
			continue
		}
		recs = append(recs, b)
		if req.Count > 0 && int32(len(recs)) >= req.Count {
			break
		}
	}
	return &booksv1.RecommendBooksResponse{
		Recommendations: recs,
		Reasoning:       fmt.Sprintf("Selected %d books matching genre=%q mood=%q", len(recs), req.Genre, req.Mood),
	}, nil
}

// ReviewBook demonstrates sampling + elicitation in a tool handler.
// In a real implementation, this would call SampleForReviewBook() and ElicitReviewApproval().
// For testing, we return a canned review.
func (s *bookService) ReviewBook(_ core.ToolContext, req *booksv1.ReviewBookRequest) (*booksv1.ReviewBookResponse, error) {
	for _, b := range catalog {
		if b.Id == req.BookId {
			return &booksv1.ReviewBookResponse{
				Review:   fmt.Sprintf("An excellent read: %s by %s", b.Title, b.Author),
				Approved: true,
			}, nil
		}
	}
	return nil, fmt.Errorf("book %q not found", req.BookId)
}

// CompleteBookId returns book ID suggestions matching the partial input.
func (s *bookService) CompleteBookId(_ core.PromptContext, _ core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
	var values []string
	for _, b := range catalog {
		if strings.HasPrefix(b.Id, arg.Value) || strings.Contains(strings.ToLower(b.Title), strings.ToLower(arg.Value)) {
			values = append(values, b.Id)
		}
	}
	return core.CompletionResult{
		Values:  values,
		Total:   len(values),
		HasMore: false,
	}, nil
}

func main() {
	impl := &bookService{}

	srv := server.NewServer(core.ServerInfo{
		Name:    "bookservice",
		Version: "0.1.0",
	})

	// Register all MCP primitives from the generated code.
	booksv1.RegisterBookServiceMCP(srv, impl)
	booksv1.RegisterBookServiceMCPResources(srv, impl)
	booksv1.RegisterBookServiceMCPPrompts(srv, impl)
	booksv1.RegisterBookServiceMCPCompletions(srv, impl)

	log.Println("Book service MCP server starting on :8787")
	if err := srv.Run(":8787"); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
