// Command eval runs the Day 5 offline evaluation suite: it prepares a
// target stack, injects each case's fault, runs the real bin/agent
// diagnose CLI, asserts the deterministic rule diagnosis and writes a
// JSON report under .run/evals/.
//
// Usage:
//
//	go run ./cmd/eval [--mode auto|docker|local] [--case <id>] [--keep] [--model]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/evals"
)

func main() {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	mode := fs.String("mode", "auto", "target environment: auto|docker|local")
	only := fs.String("case", "", "run only the case with this id")
	keep := fs.Bool("keep", false, "leave the target stack running after the suite")
	useModel := fs.Bool("model", false, "exercise the LLM diagnosis path (rules baseline when unset)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, err := evals.Run(ctx, evals.Options{
		Mode:      evals.Mode(*mode),
		OnlyCase:  *only,
		KeepStack: *keep,
		UseModel:  *useModel,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: %v\n", err)
		os.Exit(1)
	}
}
