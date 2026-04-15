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

// bookService implements the generated BookServiceMCPServer, BookServiceMCPResourceServer,
// and BookServiceMCPPromptServer interfaces.
type bookService struct{}

func (s *bookService) SearchBooks(_ core.ToolContext, req *booksv1.SearchBooksRequest) (*booksv1.SearchBooksResponse, error) {
	var results []*booksv1.Book
	query := strings.ToLower(req.Query)
	for _, b := range catalog {
		if req.Genre != "" && b.Genre != req.Genre {
			continue
		}
		// Match on title, author display name, synopsis, or genre.
		if query != "" {
			authorName := strings.ToLower(authors[b.Author])
			haystack := strings.ToLower(b.Title) + " " + authorName + " " + strings.ToLower(b.Synopsis) + " " + b.Genre
			if !strings.Contains(haystack, query) {
				continue
			}
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
	if len(books) == 0 {
		return nil, fmt.Errorf("author %q not found (try: %s)", req.AuthorId, authorList())
	}
	return &booksv1.GetAuthorBooksResponse{Books: books}, nil
}

func authorList() string {
	var ids []string
	for id := range authors {
		ids = append(ids, id)
	}
	return strings.Join(ids, ", ")
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
// Uses the generated SampleForReviewBook() to ask the LLM to write a review,
// then ElicitReviewApproval() to get user confirmation before returning.
func (s *bookService) ReviewBook(ctx core.ToolContext, req *booksv1.ReviewBookRequest) (*booksv1.ReviewBookResponse, error) {
	var book *booksv1.Book
	for _, b := range catalog {
		if b.Id == req.BookId {
			book = b
			break
		}
	}
	if book == nil {
		return nil, fmt.Errorf("book %q not found", req.BookId)
	}

	// Step 1: Use sampling to ask the LLM to generate a review.
	reviewText := fmt.Sprintf("A compelling read: %s by %s", book.Title, authors[book.Author])
	sampleResult, err := booksv1.SampleForReviewBook(ctx, []core.SamplingMessage{{
		Role: "user",
		Content: core.Content{Type: "text", Text: fmt.Sprintf(
			"Write a brief review of '%s' by %s. Synopsis: %s",
			book.Title, authors[book.Author], book.Synopsis,
		)},
	}})
	if err == nil && sampleResult.Content.Text != "" {
		reviewText = sampleResult.Content.Text
	}

	// Step 2: Use elicitation to ask the user to approve the review.
	approval, action, err := booksv1.ElicitReviewApproval(ctx,
		fmt.Sprintf("Review of '%s':\n\n%s\n\nApprove this review?", book.Title, reviewText))
	if err != nil {
		// Elicitation not supported or failed — auto-approve.
		return &booksv1.ReviewBookResponse{Review: reviewText, Approved: true}, nil
	}
	if action != "accept" || approval == nil {
		return &booksv1.ReviewBookResponse{Review: reviewText, Approved: false}, nil
	}

	return &booksv1.ReviewBookResponse{
		Review:   reviewText,
		Approved: approval.Approved,
	}, nil
}

// CompleteBookId returns book ID suggestions matching the partial input.
// Matches on ID prefix, title substring, or author name.
func (s *bookService) CompleteBookId(_ core.PromptContext, _ core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
	var values []string
	query := strings.ToLower(arg.Value)
	for _, b := range catalog {
		if strings.HasPrefix(b.Id, arg.Value) ||
			strings.Contains(strings.ToLower(b.Title), query) ||
			strings.Contains(strings.ToLower(authors[b.Author]), query) {
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

	log.Println("Book service MCP server starting on :8080")
	if err := srv.Run(":8080"); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
