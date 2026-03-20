package http

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/flarexio/claude-runner"
)

func AddRouters(r *gin.Engine, endpoints runner.EndpointSet) {
	api := r.Group("/api")

	api.POST("/run", runHandler(endpoints))
	api.POST("/async-run", asyncRunHandler(endpoints))
}

func runHandler(endpoints runner.EndpointSet) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req runner.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		ctx := c.Request.Context()

		resp, err := endpoints.Run(ctx, req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, resp)
	}
}

func asyncRunHandler(endpoints runner.EndpointSet) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req runner.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		ctx := c.Request.Context()

		done := make(chan *runner.Result, 1)
		callback := func(result *runner.Result) {
			done <- result
		}

		asyncReq := runner.AsyncRunRequest{
			Request:  req,
			Callback: callback,
		}

		resp, err := endpoints.AsyncRun(ctx, asyncReq)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		// Send ID immediately
		data, _ := json.Marshal(resp)
		fmt.Fprintf(c.Writer, "event: submitted\ndata: %s\n\n", data)
		c.Writer.Flush()

		// Wait for result
		select {
		case result := <-done:
			data, _ := json.Marshal(result)
			fmt.Fprintf(c.Writer, "event: result\ndata: %s\n\n", data)
			c.Writer.Flush()
		case <-ctx.Done():
			fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\":\"request cancelled\"}\n\n")
			c.Writer.Flush()
		}
	}
}
