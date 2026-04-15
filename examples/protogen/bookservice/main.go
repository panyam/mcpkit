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
	{Id: "1", Title: "The Go Programming Language", Author: "donovan-kernighan", Genre: "programming", Year: 2015, Rating: 4.5,
		Synopsis: "A comprehensive guide to Go covering the language specification, standard library, and best practices. Covers concurrency with goroutines and channels, interfaces, testing, and low-level programming."},
	{Id: "2", Title: "Designing Data-Intensive Applications", Author: "kleppmann", Genre: "systems", Year: 2017, Rating: 4.8,
		Synopsis: "A deep dive into the internals of databases, stream processing, batch processing, and distributed systems. Covers replication, partitioning, transactions, consensus, and the trade-offs behind modern data architectures."},
	{Id: "3", Title: "The Pragmatic Programmer", Author: "hunt-thomas", Genre: "programming", Year: 1999, Rating: 4.6,
		Synopsis: "Classic software engineering wisdom covering DRY, orthogonality, tracer bullets, domain languages, estimation, and pragmatic approaches to testing, debugging, and team collaboration."},
	{Id: "4", Title: "Structure and Interpretation of Computer Programs", Author: "abelson-sussman", Genre: "cs-theory", Year: 1996, Rating: 4.7,
		Synopsis: "Foundational CS text using Scheme to explore abstraction, recursion, higher-order functions, metalinguistic abstraction, and the design of interpreters and compilers."},
	{Id: "5", Title: "Clean Code", Author: "martin", Genre: "programming", Year: 2008, Rating: 4.3,
		Synopsis: "Principles and patterns for writing readable, maintainable code. Covers naming, functions, comments, formatting, error handling, unit testing, and refactoring with Java examples."},
	{Id: "6", Title: "Site Reliability Engineering", Author: "beyer-jones-petoff-murphy", Genre: "systems", Year: 2016, Rating: 4.4,
		Synopsis: "Google's approach to running production systems at scale. Covers SLOs, error budgets, toil reduction, monitoring, incident response, capacity planning, and release engineering."},
	{Id: "7", Title: "Distributed Systems", Author: "van-steen-tanenbaum", Genre: "systems", Year: 2017, Rating: 4.2,
		Synopsis: "Comprehensive textbook on distributed systems covering architectures, processes, communication, naming, coordination, consistency, replication, and fault tolerance."},
	{Id: "8", Title: "Introduction to Algorithms", Author: "cormen-leiserson-rivest-stein", Genre: "cs-theory", Year: 2009, Rating: 4.4,
		Synopsis: "The definitive algorithms textbook covering sorting, graph algorithms, dynamic programming, greedy algorithms, NP-completeness, and advanced data structures."},
	{Id: "9", Title: "Refactoring", Author: "fowler", Genre: "programming", Year: 2018, Rating: 4.5,
		Synopsis: "A catalog of refactoring techniques for improving the design of existing code. Covers code smells, composing methods, simplifying conditional expressions, and dealing with generalization."},
	{Id: "10", Title: "Database Internals", Author: "petrov", Genre: "systems", Year: 2019, Rating: 4.6,
		Synopsis: "Explores the internal workings of database storage engines and distributed database systems. Covers B-trees, LSM-trees, page structures, buffer management, leader election, and gossip protocols."},
	{Id: "11", Title: "Gödel, Escher, Bach", Author: "hofstadter", Genre: "cs-theory", Year: 1979, Rating: 4.7,
		Synopsis: "An exploration of consciousness, self-reference, and formal systems through an interplay of mathematics, art, and music. Connects Gödel's incompleteness theorems, Escher's drawings, and Bach's fugues."},
	{Id: "12", Title: "The Mythical Man-Month", Author: "brooks", Genre: "programming", Year: 1975, Rating: 4.3,
		Synopsis: "Classic essays on software engineering management. Introduces Brooks's Law ('adding manpower to a late project makes it later'), the surgical team model, and the second-system effect."},
}

// authors maps author IDs to display names for resource lookups.
var authors = map[string]string{
	"donovan-kernighan":          "Alan Donovan & Brian Kernighan",
	"kleppmann":                  "Martin Kleppmann",
	"hunt-thomas":                "Andrew Hunt & David Thomas",
	"abelson-sussman":            "Harold Abelson & Gerald Sussman",
	"martin":                     "Robert C. Martin",
	"beyer-jones-petoff-murphy":  "Betsy Beyer, Chris Jones, Jennifer Petoff & Niall Murphy",
	"van-steen-tanenbaum":        "Maarten van Steen & Andrew Tanenbaum",
	"cormen-leiserson-rivest-stein": "Thomas Cormen, Charles Leiserson, Ronald Rivest & Clifford Stein",
	"fowler":                     "Martin Fowler",
	"petrov":                     "Alex Petrov",
	"hofstadter":                 "Douglas Hofstadter",
	"brooks":                     "Frederick Brooks",
}

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
