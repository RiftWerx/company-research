// Package mcp implements the MCP server and tool handlers for company-research.
package mcp

import (
	"github.com/mark3labs/mcp-go/server"

	"github.com/riftwerx/company-research/internal/version"
)

// serverName is the MCP server identity name sent to clients during initialisation.
const serverName = "company-research"

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
	mcpServer := server.NewMCPServer(serverName, version.Version,
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
