package runner

import (
	"context"

	"go.uber.org/zap"
)

func LoggingMiddleware(log *zap.Logger) ServiceMiddleware {
	log = log.With(
		zap.String("service", "claude-runner"),
	)

	return func(next Service) Service {
		log.Info("service initialized")

		return &loggingMiddleware{
			log:  log,
			next: next,
		}
	}
}

type loggingMiddleware struct {
	log  *zap.Logger
	next Service
}

func (mw *loggingMiddleware) Close() error {
	return mw.next.Close()
}

func (mw *loggingMiddleware) Run(ctx context.Context, req Request) (*Result, error) {
	log := mw.log.With(
		zap.String("action", "run"),
		zap.String("repo", req.Repo),
		zap.String("ref", req.Ref),
		zap.String("base_ref", req.BaseRef),
		zap.String("event", req.Event),
		zap.Int("pr_number", req.PRNumber),
		zap.Bool("has_diff", req.Diff != ""),
	)

	result, err := mw.next.Run(ctx, req)
	if err != nil {
		log.Error(err.Error())
		return nil, err
	}

	log.Info("run completed",
		zap.String("id", result.ID),
		zap.Bool("has_error", result.Error != ""),
	)

	return result, nil
}
