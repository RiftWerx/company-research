package main

import (
	"fmt"
	"log"
	"os"

	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/client"
	"github.com/riftwerx/company-research/internal/companyhouse"
	mcpserver "github.com/riftwerx/company-research/internal/mcp"
)

// userAgent is sent in the User-Agent header of every outbound HTTP request.
var userAgent = "company-research/" + mcpserver.Version

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(mcpserver.Version)
		return
	}

	apiKey := os.Getenv("CH_API_KEY")
	if apiKey == "" {
		log.Fatal("CH_API_KEY environment variable not set")
	}

	httpClient := client.New(client.Config{
		Rate:      companyhouse.DefaultRate,
		Burst:     companyhouse.DefaultBurst,
		Timeout:   companyhouse.DefaultTimeout,
		UserAgent: userAgent,
	})

	chSvc := companyhouse.New(httpClient, apiKey)

	filingCache, err := cache.New(cache.NewDefaultConfig())
	if err != nil {
		log.Fatalf("open cache: %v", err)
	}
	defer filingCache.Close()

	srv := mcpserver.New(chSvc, filingCache)

	if err := srv.Serve(); err != nil {
		log.Fatal(err)
	}
}
