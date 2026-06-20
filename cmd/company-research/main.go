package main

import (
	"context"
	"log"
	"os"

	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/cli"
	"github.com/riftwerx/company-research/internal/client"
	"github.com/riftwerx/company-research/internal/companyhouse"
	mcpserver "github.com/riftwerx/company-research/internal/mcp"
	"github.com/riftwerx/company-research/internal/version"
)

// userAgent is sent in the User-Agent header of every outbound HTTP request.
var userAgent = "company-research/" + version.Version

func main() {
	// No arguments → MCP stdio mode (existing behaviour).
	if len(os.Args) == 1 {
		runMCP()
		return
	}

	// CLI mode: parse arguments first. kong.Parse exits for --help, --version,
	// and unknown commands, so the API key is only checked when a real subcommand
	// was successfully parsed.
	kctx := cli.Parse()

	// install-skill is a setup command; it does not need CH_API_KEY or services.
	if kctx.Command() == "install-skill" {
		if err := cli.Execute(context.Background(), kctx); err != nil {
			log.Fatal(err)
		}
		return
	}

	apiKey := os.Getenv("CH_API_KEY")
	if apiKey == "" {
		log.Fatal("CH_API_KEY environment variable not set")
	}

	chSvc, filingCache, err := buildServices(apiKey)
	if err != nil {
		log.Fatalf("open cache: %v", err)
	}
	defer filingCache.Close()

	if err := cli.Execute(context.Background(), kctx, chSvc, filingCache); err != nil {
		log.Fatal(err)
	}
}

func runMCP() {
	apiKey := os.Getenv("CH_API_KEY")
	if apiKey == "" {
		log.Fatal("CH_API_KEY environment variable not set")
	}

	chSvc, filingCache, err := buildServices(apiKey)
	if err != nil {
		log.Fatalf("open cache: %v", err)
	}
	defer filingCache.Close()

	srv := mcpserver.New(chSvc, filingCache)

	if err := srv.Serve(); err != nil {
		log.Fatal(err)
	}
}

func buildServices(apiKey string) (*companyhouse.Service, *cache.Cache, error) {
	httpClient := client.New(client.Config{
		Rate:      companyhouse.DefaultRate,
		Burst:     companyhouse.DefaultBurst,
		Timeout:   companyhouse.DefaultTimeout,
		UserAgent: userAgent,
	})
	chSvc := companyhouse.New(httpClient, apiKey)
	filingCache, err := cache.New(cache.NewDefaultConfig())
	if err != nil {
		return nil, nil, err
	}
	return chSvc, filingCache, nil
}
