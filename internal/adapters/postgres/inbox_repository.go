package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/ingestion"
)

type InboxRepository struct{}

func NewInboxRepository() *InboxRepository { return &InboxRepository{} }

func (r *InboxRepository) Claim(
	ctx context.Context,
	db DBTX,
	consumer, messageID string,
	hash application.PayloadHash,
	receivedAt time.Time,
) (application.Claim, error) {
	if _, err := db.Exec(ctx, `
		INSERT INTO consumer_inbox(consumer_name, message_id, payload_hash, received_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		consumer, messageID, hash[:], receivedAt.UTC()); err != nil {
		return application.Claim{}, fmt.Errorf("insert inbox message: %w", err)
	}
	var storedHash []byte
	var completedAt pgtype.Timestamptz
	var transactionID pgtype.Text
	err := db.QueryRow(ctx, `
		SELECT payload_hash, completed_at, transaction_id::text
		FROM consumer_inbox
		WHERE consumer_name=$1 AND message_id=$2
		FOR UPDATE`, consumer, messageID).Scan(&storedHash, &completedAt, &transactionID)
	if err != nil {
		return application.Claim{}, fmt.Errorf("lock inbox message: %w", err)
	}
	if !bytes.Equal(storedHash, hash[:]) {
		return application.Claim{}, application.ErrMessageConflict
	}
	if completedAt.Valid && !transactionID.Valid {
		return application.Claim{}, errors.New("completed inbox message has no transaction")
	}
	return application.Claim{Completed: completedAt.Valid, TransactionID: transactionID.String}, nil
}

func (r *InboxRepository) Complete(
	ctx context.Context,
	db DBTX,
	consumer, messageID, transactionID string,
	completedAt time.Time,
) error {
	tag, err := db.Exec(ctx, `
		UPDATE consumer_inbox
		SET completed_at=$3, transaction_id=$4
		WHERE consumer_name=$1 AND message_id=$2 AND completed_at IS NULL`,
		consumer, messageID, completedAt.UTC(), transactionID)
	if err != nil {
		return fmt.Errorf("complete inbox message: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentWrite
	}
	return nil
}
