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

func (mw *loggingMiddleware) AsyncRun(ctx context.Context, req Request, fn ResultFunc) (string, error) {
	log := mw.log.With(
		zap.String("action", "async-run"),
		zap.String("repo", req.Repo),
		zap.String("ref", req.Ref),
	)

	id, err := mw.next.AsyncRun(ctx, req, fn)
	if err != nil {
		log.Error(err.Error())
		return "", err
	}

	log.Info("async run started", zap.String("id", id))

	return id, nil
}

func (mw *loggingMiddleware) Run(ctx context.Context, req Request) (*Result, error) {
	log := mw.log.With(
		zap.String("action", "run"),
		zap.String("repo", req.Repo),
		zap.String("ref", req.Ref),
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
