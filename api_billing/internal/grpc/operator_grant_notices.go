package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
)

// sweepOperatorGrantNoticesOnce queues ending notices for grants that end
// within the lead time, drops unsent ones whose grant no longer ends then,
// and delivers what is due. Ended notices are queued when the grant is removed.
func (s *PurserServer) sweepOperatorGrantNoticesOnce(ctx context.Context) {
	defer s.observeOperatorGrantNotices(ctx)
	queries := purserdb.New(s.db)
	if _, err := queries.DiscardStaleOperatorGrantEndingNotices(ctx); err != nil {
		s.operatorGrantNoticeReadFailed()
		s.logger.WithError(err).Warn("Could not discard stale operator grant ending notices; retrying on the next sweep")
	}
	if _, err := queries.EnqueueOperatorGrantEndingNotices(ctx, int64(handlers.OperatorGrantNoticeLead/time.Second)); err != nil {
		s.operatorGrantNoticeReadFailed()
		s.logger.WithError(err).Warn("Could not queue operator grant ending notices; retrying on the next sweep")
	}
	ids, err := queries.ListOperatorGrantNotices(ctx)
	if err != nil {
		s.operatorGrantNoticeReadFailed()
		s.logger.WithError(err).Warn("Could not list operator grant notices; retrying on the next sweep")
		return
	}
	for _, id := range ids {
		if noticeErr := s.deliverOperatorGrantNotice(ctx, id); noticeErr != nil {
			s.logger.WithError(noticeErr).WithField("notice_id", id).Warn("Operator grant notice delivery failed; frameworks cluster doctor --deep lists failing notices")
		}
	}
}

// deliverOperatorGrantNotice sends outside a transaction after claiming the
// notice. Failed sends back off and are abandoned after five attempts.
func (s *PurserServer) deliverOperatorGrantNotice(ctx context.Context, id string) error {
	queries := purserdb.New(s.db)
	row, err := queries.ClaimOperatorGrantNotice(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.BillingEmail != "" {
		if sendErr := s.sendOperatorGrantNotice(handlers.OperatorGrantNotice{
			Recipient: row.BillingEmail, TenantName: row.BillingName, Kind: row.Kind, EndsAt: row.EndsAt, PlanName: row.PlanName,
		}); sendErr != nil {
			if recordErr := queries.RecordOperatorGrantNoticeFailure(ctx, purserdb.RecordOperatorGrantNoticeFailureParams{
				ID: id, AttemptedAt: row.AttemptedAt, Failure: sendErr.Error(),
			}); recordErr != nil {
				return errors.Join(sendErr, recordErr)
			}
			return sendErr
		}
	}
	return queries.CompleteOperatorGrantNotice(ctx, purserdb.CompleteOperatorGrantNoticeParams{ID: id, AttemptedAt: row.AttemptedAt})
}

func (s *PurserServer) sendOperatorGrantNotice(notice handlers.OperatorGrantNotice) error {
	if s.operatorGrantNoticeSender != nil {
		return s.operatorGrantNoticeSender(notice)
	}
	return s.billing.SendOperatorGrantNotice(notice)
}

// observeOperatorGrantNotices retains the last good gauge through database outages.
func (s *PurserServer) observeOperatorGrantNotices(ctx context.Context) {
	if s.metrics == nil || s.metrics.OperatorGrantNoticeFailures == nil {
		return
	}
	count, err := purserdb.New(s.db).CountRepeatedOperatorGrantNoticeFailures(ctx)
	if err != nil {
		s.operatorGrantNoticeReadFailed()
		return
	}
	s.metrics.OperatorGrantNoticeFailures.WithLabelValues().Set(float64(count))
}

func (s *PurserServer) operatorGrantNoticeReadFailed() {
	if s.metrics != nil && s.metrics.OperatorGrantNoticeReadFailures != nil {
		s.metrics.OperatorGrantNoticeReadFailures.WithLabelValues().Inc()
	}
}
