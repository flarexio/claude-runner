package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-kit/kit/endpoint"

	runner "github.com/flarexio/claude-runner"
)

func AddRouters(r *gin.Engine, endpoints runner.EndpointSet) {
	api := r.Group("/api")

	api.POST("/run", endpointHandler(endpoints.Run))
	api.POST("/run-issue", endpointHandler(endpoints.RunIssue))
}

func endpointHandler(ep endpoint.Endpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req runner.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		ctx := c.Request.Context()

		resp, err := ep(ctx, req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, resp)
	}
}
