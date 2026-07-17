//go:build eval_llm

// This file is excluded from the default build and CI path: the LLM-as-judge
// scorer needs a live Provider (a real model), so it sits behind the eval_llm
// build tag alongside its test. The default deterministic scorers carry no
// model or network requirement. Build or test it with: go test -tags eval_llm ./...

package eval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// judgeVerdict is the structured grade the judge model returns.
type judgeVerdict struct {
	Pass   bool    `json:"pass"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// Judge grades a Result by asking a model to evaluate the rendered transcript
// against rubric. It is the LLM-as-judge scorer: non-deterministic, requiring
// a live Provider, hence build-tagged off the default path. The model is asked
// for a structured {pass, score, reason} verdict via ResponseSchema.
//
// The Scorer interface carries no context, so the judge grades under
// context.Background(); wrap the provider with a timeout if you need to bound
// it. Any transport, decode, or empty-response failure yields a failing Score
// carrying the reason, so a broken judge fails loudly rather than silently
// passing a case.
func Judge(provider agent.Provider, rubric string) Scorer {
	schema := core.NewRawJSON(json.RawMessage(core.GenerateSchema[judgeVerdict]()))
	return scorerFunc(func(r Result) Score {
		req := agent.ProviderRequest{
			Instructions: "You are a strict evaluator. Grade the agent transcript against the " +
				"rubric. Respond only with the JSON verdict: pass (bool), score (0..1), reason (string).",
			Messages: []agent.Message{{
				Role: agent.RoleUser,
				Text: fmt.Sprintf("Rubric:\n%s\n\nTranscript:\n%s", rubric, Transcript(r)),
			}},
			ResponseSchema: schema,
		}

		resp, err := provider.Generate(context.Background(), req)
		if err != nil {
			return boolScore("Judge", false, "judge model error: "+err.Error())
		}
		if resp == nil || resp.Text == "" {
			return boolScore("Judge", false, "judge returned no verdict")
		}

		var v judgeVerdict
		if err := json.Unmarshal([]byte(resp.Text), &v); err != nil {
			return boolScore("Judge", false, "judge verdict was not valid JSON: "+err.Error())
		}
		return Score{Name: "Judge", Pass: v.Pass, Value: v.Score, Detail: v.Reason}
	})
}
