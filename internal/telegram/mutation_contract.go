package telegram

import (
	"fmt"

	"github.com/gotd/td/tg"
)

type MutationResult struct {
	OK                   bool            `json:"ok"`
	Outcome              MutationOutcome `json:"outcome"`
	RetrySafe            bool            `json:"retry_safe"`
	Action               string          `json:"action"`
	PeerRef              string          `json:"peer_ref"`
	MessageID            int             `json:"message_id,omitempty"`
	MessageIDs           []int           `json:"message_ids,omitempty"`
	ReconciliationHandle string          `json:"reconciliation_handle"`
	Timestamp            string          `json:"timestamp"`
}

type MutationPreview struct {
	OK        bool        `json:"ok"`
	DryRun    bool        `json:"dry_run"`
	Action    string      `json:"action"`
	PeerRef   string      `json:"peer_ref"`
	MessageID int         `json:"message_id,omitempty"`
	Scope     DeleteScope `json:"scope,omitempty"`
	Timestamp string      `json:"timestamp"`
}

type MutationOutcome string

const (
	MutationConfirmed      MutationOutcome = "confirmed"
	MutationRejected       MutationOutcome = "rejected"
	MutationOutcomeUnknown MutationOutcome = "outcome_unknown"
)

type MutationError struct {
	Outcome              MutationOutcome
	RetrySafe            bool
	ReconciliationHandle string
	Err                  error
}

func (e MutationError) Error() string {
	if e.Outcome == MutationOutcomeUnknown {
		return fmt.Sprintf("mutation outcome unknown; do not retry blindly: %v", e.Err)
	}
	if e.Outcome == MutationConfirmed {
		return fmt.Sprintf("mutation confirmed but receipt output failed; do not retry: %v", e.Err)
	}
	return e.Err.Error()
}

func (e MutationError) Unwrap() error { return e.Err }

func (e MutationError) MutationOutcomeCode() string { return string(e.Outcome) }

func (e MutationError) MutationRetrySafe() bool { return e.RetrySafe }

func (e MutationError) MutationReconciliationHandle() string { return e.ReconciliationHandle }

func ConfirmedMutationOutputError(result MutationResult, err error) error {
	return MutationError{
		Outcome:              MutationConfirmed,
		RetrySafe:            false,
		ReconciliationHandle: result.ReconciliationHandle,
		Err:                  err,
	}
}

type DeleteScope string

const (
	DeleteScopeForMe  DeleteScope = "for_me"
	DeleteScopeRevoke DeleteScope = "revoke"
)

type deleteRoute string

const (
	deleteRouteMessages deleteRoute = "messages.deleteMessages"
	deleteRouteChannels deleteRoute = "channels.deleteMessages"
)

type deletePlan struct {
	Route   deleteRoute
	Channel *tg.InputChannel
	Revoke  bool
}
