// Package mcp implements the MCP server and tool handlers for company-research.
package mcp

import (
	"github.com/mark3labs/mcp-go/server"
)

// serverName is the MCP server identity name sent to clients during initialisation.
const serverName = "company-research"

// Version is the server version, exported for use in the binary's user-agent string.
// "dev" is the default for local builds; release builds have this overridden via
// -ldflags "-X github.com/riftwerx/company-research/internal/mcp.Version=<tag>"
// by goreleaser (or make local-release).
var Version = "dev"

// Server holds the dependencies used by all tool handlers.
// The zero value is not usable; construct with New.
type Server struct {
	chSvc CompanyHouseService
	cache FilingCache
}

// New constructs a Server backed by the given Companies House service and filing cache.
func New(chSvc CompanyHouseService, cache FilingCache) *Server {
	return &Server{chSvc: chSvc, cache: cache}
}

// Serve registers all CH tools and starts the MCP stdio server.
// It blocks until the client disconnects or a signal is received.
func (s *Server) Serve() error {
	mcpServer := server.NewMCPServer(serverName, Version,
		server.WithToolCapabilities(false),
	)

	mcpServer.AddTool(searchCompanyTool(), s.handleSearchCompany)
	mcpServer.AddTool(getCompanyProfileTool(), s.handleGetCompanyProfile)
	mcpServer.AddTool(listFilingsTool(), s.handleListFilings)
	mcpServer.AddTool(fetchFilingTool(), s.handleFetchFiling)
	mcpServer.AddTool(getLatestTool(), s.handleGetLatest)
	mcpServer.AddTool(listZipContentsTool(), s.handleListZipContents)
	mcpServer.AddTool(extractXBRLFactsTool(), s.handleExtractXBRLFacts)
	mcpServer.AddTool(clearCacheTool(), s.handleClearCache)

	return server.ServeStdio(mcpServer)
}
